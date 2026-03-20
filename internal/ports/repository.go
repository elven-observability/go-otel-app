package ports

import (
	"context"

	"github.com/elven-observability/go-otel-app/internal/domain"
)

type OrderRepository interface {
	Create(ctx context.Context, order domain.Order) (domain.Order, error)
	GetByID(ctx context.Context, id string) (domain.Order, error)
	UpdateStatus(ctx context.Context, id string, status domain.OrderStatus, failureReason *string) error
	Ping(ctx context.Context) error
}
