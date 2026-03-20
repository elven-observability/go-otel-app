package ports

import (
	"context"
	"errors"
	"time"

	"github.com/elven-observability/go-otel-app/internal/domain"
)

var ErrQueueEmpty = errors.New("queue empty")

type FulfillmentQueue interface {
	Enqueue(ctx context.Context, job domain.FulfillmentJob) error
	Dequeue(ctx context.Context, wait time.Duration) (domain.FulfillmentJob, error)
	SendToDLQ(ctx context.Context, job domain.FulfillmentJob, reason string) error
	Depth(ctx context.Context) (int64, error)
	Ping(ctx context.Context) error
}
