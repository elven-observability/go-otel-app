package application

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/elven-observability/go-otel-app/internal/domain"
	"github.com/elven-observability/go-otel-app/internal/ports"
)

type ProcessFulfillmentUseCase struct {
	Repo        ports.OrderRepository
	Cache       ports.OrderCache
	Queue       ports.FulfillmentQueue
	Shipping    ports.ShippingGateway
	Metrics     ports.Metrics
	Tracer      trace.Tracer
	MaxAttempts int
}

func (uc ProcessFulfillmentUseCase) Execute(ctx context.Context, job domain.FulfillmentJob) error {
	start := time.Now()
	ctx, span := uc.Tracer.Start(ctx, "worker.fulfillment.process")
	defer span.End()

	if job.WorkerLatencyMS > 0 {
		select {
		case <-ctx.Done():
			recordSpanError(span, ctx.Err())
			uc.Metrics.RecordJobDuration(ctx, time.Since(start), "cancelled")
			return ctx.Err()
		case <-time.After(time.Duration(job.WorkerLatencyMS) * time.Millisecond):
		}
	}

	order, err := uc.Repo.GetByID(ctx, job.OrderID)
	if err != nil {
		recordSpanError(span, err)
		uc.Metrics.RecordJobDuration(ctx, time.Since(start), "failed")
		uc.Metrics.IncrementOrderFailed(ctx, "worker_load_order", errorType(err))
		return err
	}

	if err := uc.Shipping.Book(ctx, order, job); err != nil {
		recordSpanError(span, err)
		if uc.shouldRetry(job) {
			retryJob := job
			retryJob.Attempt++
			if enqueueErr := uc.Queue.Enqueue(ctx, retryJob); enqueueErr != nil {
				recordSpanError(span, enqueueErr)
				return enqueueErr
			}

			uc.Metrics.RecordJobDuration(ctx, time.Since(start), "retry")
			uc.Metrics.IncrementOrderFailed(ctx, "shipping_retry", errorType(err))
			return err
		}

		if persistErr := uc.persistResult(ctx, order.ID, domain.OrderStatusFailed, err.Error()); persistErr != nil {
			recordSpanError(span, persistErr)
			return persistErr
		}
		if dlqErr := uc.Queue.SendToDLQ(ctx, job, err.Error()); dlqErr != nil {
			recordSpanError(span, dlqErr)
			return dlqErr
		}

		uc.Metrics.RecordJobDuration(ctx, time.Since(start), "failed")
		uc.Metrics.IncrementOrderFailed(ctx, "shipping", errorType(err))
		return err
	}

	if err := uc.persistResult(ctx, order.ID, domain.OrderStatusFulfilled, ""); err != nil {
		recordSpanError(span, err)
		uc.Metrics.RecordJobDuration(ctx, time.Since(start), "failed")
		uc.Metrics.IncrementOrderFailed(ctx, "worker_persist", errorType(err))
		return err
	}

	uc.Metrics.RecordJobDuration(ctx, time.Since(start), "success")
	return nil
}

func (uc ProcessFulfillmentUseCase) shouldRetry(job domain.FulfillmentJob) bool {
	maxAttempts := uc.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	return job.Attempt < maxAttempts
}

func (uc ProcessFulfillmentUseCase) persistResult(ctx context.Context, orderID string, status domain.OrderStatus, failureReason string) error {
	ctx, span := uc.Tracer.Start(ctx, "worker.fulfillment.persist_result")
	defer span.End()

	var reason *string
	if failureReason != "" {
		reason = &failureReason
	}

	if err := uc.Repo.UpdateStatus(ctx, orderID, status, reason); err != nil {
		recordSpanError(span, err)
		return err
	}

	order, err := uc.Repo.GetByID(ctx, orderID)
	if err != nil {
		recordSpanError(span, err)
		return err
	}

	if err := uc.Cache.Set(ctx, order); err != nil {
		recordSpanError(span, err)
		return err
	}

	return nil
}
