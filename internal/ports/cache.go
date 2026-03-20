package ports

import (
	"context"

	"github.com/elven-observability/go-otel-app/internal/domain"
)

type OrderCache interface {
	Set(ctx context.Context, order domain.Order) error
	Get(ctx context.Context, id string) (domain.Order, error)
	Ping(ctx context.Context) error
}
