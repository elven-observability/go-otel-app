package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/elven-observability/go-otel-app/internal/domain"
	"github.com/elven-observability/go-otel-app/internal/ports"
)

type CreateOrderUseCase struct {
	Repo      ports.OrderRepository
	Cache     ports.OrderCache
	Queue     ports.FulfillmentQueue
	Payment   ports.PaymentGateway
	Inventory ports.InventoryGateway
	Metrics   ports.Metrics
	Tracer    trace.Tracer
}

func (uc CreateOrderUseCase) Execute(ctx context.Context, input domain.CreateOrderInput) (domain.Order, error) {
	start := time.Now()
	ctx, span := uc.Tracer.Start(ctx, "order.create")
	defer span.End()

	order, err := uc.validateAndBuild(ctx, input)
	if err != nil {
		recordSpanError(span, err)
		uc.Metrics.RecordOrderCreate(ctx, time.Since(start), "failed")
		uc.Metrics.IncrementOrderFailed(ctx, "validate", errorType(err))
		return domain.Order{}, err
	}

	persisted, err := uc.persist(ctx, order)
	if err != nil {
		recordSpanError(span, err)
		uc.Metrics.RecordOrderCreate(ctx, time.Since(start), "failed")
		uc.Metrics.IncrementOrderFailed(ctx, "persist", errorType(err))
		return domain.Order{}, domain.NewInternalError("failed to persist order")
	}

	if err := uc.cache(ctx, persisted); err != nil {
		recordSpanError(span, err)
		_ = uc.failOrder(ctx, persisted.ID, err.Error())
		uc.Metrics.RecordOrderCreate(ctx, time.Since(start), "failed")
		uc.Metrics.IncrementOrderFailed(ctx, "cache", errorType(err))
		return domain.Order{}, domain.NewInternalError("failed to write order cache")
	}

	if err := uc.Payment.Authorize(ctx, persisted, input.Simulation); err != nil {
		recordSpanError(span, err)
		_ = uc.failOrder(ctx, persisted.ID, err.Error())
		uc.Metrics.RecordOrderCreate(ctx, time.Since(start), "failed")
		uc.Metrics.IncrementOrderFailed(ctx, "payment", errorType(err))
		return domain.Order{}, err
	}

	if err := uc.Inventory.Reserve(ctx, persisted, input.Simulation); err != nil {
		recordSpanError(span, err)
		_ = uc.failOrder(ctx, persisted.ID, err.Error())
		uc.Metrics.RecordOrderCreate(ctx, time.Since(start), "failed")
		uc.Metrics.IncrementOrderFailed(ctx, "inventory", errorType(err))
		return domain.Order{}, err
	}

	queuedOrder, err := uc.enqueueFulfillment(ctx, persisted, input.Simulation)
	if err != nil {
		recordSpanError(span, err)
		_ = uc.failOrder(ctx, persisted.ID, err.Error())
		uc.Metrics.RecordOrderCreate(ctx, time.Since(start), "failed")
		uc.Metrics.IncrementOrderFailed(ctx, "enqueue", errorType(err))
		return domain.Order{}, domain.NewInternalError("failed to enqueue fulfillment job")
	}

	uc.Metrics.RecordOrderCreate(ctx, time.Since(start), "success")
	uc.Metrics.IncrementOrderCreated(ctx)
	return queuedOrder, nil
}

func (uc CreateOrderUseCase) validateAndBuild(ctx context.Context, input domain.CreateOrderInput) (domain.Order, error) {
	ctx, span := uc.Tracer.Start(ctx, "order.create.validate")
	defer span.End()

	switch {
	case input.CustomerID == "":
		err := domain.NewValidationError("customer_id is required")
		recordSpanError(span, err)
		return domain.Order{}, err
	case input.SKU == "":
		err := domain.NewValidationError("sku is required")
		recordSpanError(span, err)
		return domain.Order{}, err
	case input.Quantity <= 0:
		err := domain.NewValidationError("quantity must be greater than zero")
		recordSpanError(span, err)
		return domain.Order{}, err
	case input.AmountCents <= 0:
		err := domain.NewValidationError("amount_cents must be greater than zero")
		recordSpanError(span, err)
		return domain.Order{}, err
	}

	now := time.Now().UTC()
	return domain.Order{
		ID:          uuid.NewString(),
		CustomerID:  input.CustomerID,
		SKU:         input.SKU,
		Quantity:    input.Quantity,
		AmountCents: input.AmountCents,
		Status:      domain.OrderStatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (uc CreateOrderUseCase) persist(ctx context.Context, order domain.Order) (domain.Order, error) {
	ctx, span := uc.Tracer.Start(ctx, "order.create.persist")
	defer span.End()

	persisted, err := uc.Repo.Create(ctx, order)
	if err != nil {
		recordSpanError(span, err)
		return domain.Order{}, err
	}

	return persisted, nil
}

func (uc CreateOrderUseCase) cache(ctx context.Context, order domain.Order) error {
	ctx, span := uc.Tracer.Start(ctx, "order.create.cache")
	defer span.End()

	if err := uc.Cache.Set(ctx, order); err != nil {
		recordSpanError(span, err)
		return err
	}

	return nil
}

func (uc CreateOrderUseCase) enqueueFulfillment(ctx context.Context, order domain.Order, simulation domain.Simulation) (domain.Order, error) {
	ctx, span := uc.Tracer.Start(ctx, "order.create.enqueue_fulfillment")
	defer span.End()

	job := domain.FulfillmentJob{
		OrderID:           order.ID,
		SKU:               order.SKU,
		Quantity:          order.Quantity,
		ShippingLatencyMS: simulation.ShippingLatencyMS,
		FailShipping:      simulation.FailShipping,
		WorkerLatencyMS:   simulation.WorkerLatencyMS,
		Attempt:           1,
	}

	if err := uc.Queue.Enqueue(ctx, job); err != nil {
		recordSpanError(span, err)
		return domain.Order{}, err
	}

	if err := uc.Repo.UpdateStatus(ctx, order.ID, domain.OrderStatusQueued, nil); err != nil {
		recordSpanError(span, err)
		return domain.Order{}, err
	}

	order.Status = domain.OrderStatusQueued
	order.UpdatedAt = time.Now().UTC()
	if err := uc.Cache.Set(ctx, order); err != nil {
		recordSpanError(span, err)
		return domain.Order{}, err
	}

	return order, nil
}

func (uc CreateOrderUseCase) failOrder(ctx context.Context, orderID, reason string) error {
	if orderID == "" {
		return nil
	}

	failureReason := reason
	if err := uc.Repo.UpdateStatus(ctx, orderID, domain.OrderStatusFailed, &failureReason); err != nil {
		return err
	}

	order, err := uc.Repo.GetByID(ctx, orderID)
	if err != nil {
		return nil
	}

	return uc.Cache.Set(ctx, order)
}

func recordSpanError(span trace.Span, err error) {
	if err == nil {
		return
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

func errorType(err error) string {
	var domainErr *domain.Error
	if ok := errorAs(err, &domainErr); ok && domainErr != nil {
		return domainErr.ErrorType
	}

	return "internal"
}
