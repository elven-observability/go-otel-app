package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/elven-observability/go-otel-app/internal/application"
	"github.com/elven-observability/go-otel-app/internal/ports"
)

type Worker struct {
	Queue       ports.FulfillmentQueue
	Processor   application.ProcessFulfillmentUseCase
	Metrics     ports.Metrics
	Logger      *slog.Logger
	PollTimeout time.Duration
}

func New(queue ports.FulfillmentQueue, processor application.ProcessFulfillmentUseCase, metrics ports.Metrics, logger *slog.Logger, pollTimeout time.Duration) *Worker {
	return &Worker{
		Queue:       queue,
		Processor:   processor,
		Metrics:     metrics,
		Logger:      logger,
		PollTimeout: pollTimeout,
	}
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		w.refreshQueueDepth(ctx)

		job, err := w.Queue.Dequeue(ctx, w.PollTimeout)
		if err != nil {
			if errors.Is(err, ports.ErrQueueEmpty) {
				continue
			}
			return err
		}

		w.Metrics.SetWorkerActive(1)
		err = w.Processor.Execute(ctx, job)
		w.Metrics.SetWorkerActive(0)
		w.refreshQueueDepth(ctx)

		if err != nil {
			w.Logger.ErrorContext(ctx, "worker failed to process job", "order_id", job.OrderID, "error", err.Error())
		}
	}
}

func (w *Worker) refreshQueueDepth(ctx context.Context) {
	depth, err := w.Queue.Depth(ctx)
	if err != nil {
		return
	}
	w.Metrics.SetQueueDepth(depth)
}
