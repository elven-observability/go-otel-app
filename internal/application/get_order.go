package application

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/elven-observability/go-otel-app/internal/domain"
	"github.com/elven-observability/go-otel-app/internal/ports"
)

type GetOrderUseCase struct {
	Repo   ports.OrderRepository
	Cache  ports.OrderCache
	Tracer trace.Tracer
}

func (uc GetOrderUseCase) Execute(ctx context.Context, id string) (domain.Order, error) {
	ctx, span := uc.Tracer.Start(ctx, "order.get")
	defer span.End()

	order, err := uc.Cache.Get(ctx, id)
	if err == nil {
		return order, nil
	}

	order, err = uc.Repo.GetByID(ctx, id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return domain.Order{}, err
	}

	_ = uc.Cache.Set(ctx, order)
	return order, nil
}
