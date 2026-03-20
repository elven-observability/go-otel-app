package ports

import (
	"context"
	"time"

	"github.com/elven-observability/go-otel-app/internal/domain"
)

type PaymentGateway interface {
	Authorize(ctx context.Context, order domain.Order, simulation domain.Simulation) error
}

type InventoryGateway interface {
	Reserve(ctx context.Context, order domain.Order, simulation domain.Simulation) error
}

type ShippingGateway interface {
	Book(ctx context.Context, order domain.Order, job domain.FulfillmentJob) error
}

type Metrics interface {
	RecordOrderCreate(ctx context.Context, duration time.Duration, status string)
	IncrementOrderCreated(ctx context.Context)
	IncrementOrderFailed(ctx context.Context, stage, errorType string)
	RecordJobDuration(ctx context.Context, duration time.Duration, status string)
	SetQueueDepth(value int64)
	SetWorkerActive(value int64)
}
