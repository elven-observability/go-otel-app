package application

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/elven-observability/go-otel-app/internal/domain"
)

func TestProcessFulfillmentSuccess(t *testing.T) {
	t.Parallel()

	repo := newFakeOrderRepository()
	cache := newFakeOrderCache()
	queue := &fakeQueue{}
	metrics := &fakeMetrics{}
	order := domain.Order{
		ID:          "order-success",
		CustomerID:  "cust-1",
		SKU:         "sku-1",
		Quantity:    1,
		AmountCents: 1000,
		Status:      domain.OrderStatusQueued,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	repo.orders[order.ID] = order
	cache.store[order.ID] = order

	useCase := ProcessFulfillmentUseCase{
		Repo:        repo,
		Cache:       cache,
		Queue:       queue,
		Shipping:    fakeShippingGateway{},
		Metrics:     metrics,
		Tracer:      testTracer(),
		MaxAttempts: 3,
	}

	err := useCase.Execute(context.Background(), domain.FulfillmentJob{
		OrderID:  order.ID,
		SKU:      order.SKU,
		Quantity: order.Quantity,
		Attempt:  1,
	})

	require.NoError(t, err)
	require.Equal(t, domain.OrderStatusFulfilled, repo.orders[order.ID].Status)
	require.Equal(t, domain.OrderStatusFulfilled, cache.store[order.ID].Status)
	require.Equal(t, "success", metrics.jobDuration[0].Status)
	require.Empty(t, queue.dlq)
}

func TestProcessFulfillmentRetriesBeforeDLQ(t *testing.T) {
	t.Parallel()

	repo := newFakeOrderRepository()
	cache := newFakeOrderCache()
	queue := &fakeQueue{}
	metrics := &fakeMetrics{}
	order := domain.Order{
		ID:          "order-retry",
		CustomerID:  "cust-2",
		SKU:         "sku-2",
		Quantity:    2,
		AmountCents: 2000,
		Status:      domain.OrderStatusQueued,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	repo.orders[order.ID] = order
	cache.store[order.ID] = order

	useCase := ProcessFulfillmentUseCase{
		Repo:        repo,
		Cache:       cache,
		Queue:       queue,
		Shipping:    fakeShippingGateway{err: domain.NewDependencyError("shipping_failed", "upstream error", "shipping_dependency")},
		Metrics:     metrics,
		Tracer:      testTracer(),
		MaxAttempts: 3,
	}

	err := useCase.Execute(context.Background(), domain.FulfillmentJob{
		OrderID:  order.ID,
		SKU:      order.SKU,
		Quantity: order.Quantity,
		Attempt:  1,
	})

	require.Error(t, err)
	require.Len(t, queue.jobs, 1)
	require.Equal(t, 2, queue.jobs[0].Attempt)
	require.Empty(t, queue.dlq)
	require.Equal(t, domain.OrderStatusQueued, repo.orders[order.ID].Status)
	require.Equal(t, "retry", metrics.jobDuration[0].Status)
	require.Equal(t, "shipping_retry", metrics.orderFailed[0].Stage)
}

func TestProcessFulfillmentSendsToDLQAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	repo := newFakeOrderRepository()
	cache := newFakeOrderCache()
	queue := &fakeQueue{}
	metrics := &fakeMetrics{}
	order := domain.Order{
		ID:          "order-dlq",
		CustomerID:  "cust-3",
		SKU:         "sku-3",
		Quantity:    3,
		AmountCents: 3000,
		Status:      domain.OrderStatusQueued,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	repo.orders[order.ID] = order
	cache.store[order.ID] = order

	useCase := ProcessFulfillmentUseCase{
		Repo:        repo,
		Cache:       cache,
		Queue:       queue,
		Shipping:    fakeShippingGateway{err: domain.NewDependencyError("shipping_failed", "still failing", "shipping_dependency")},
		Metrics:     metrics,
		Tracer:      testTracer(),
		MaxAttempts: 3,
	}

	err := useCase.Execute(context.Background(), domain.FulfillmentJob{
		OrderID:  order.ID,
		SKU:      order.SKU,
		Quantity: order.Quantity,
		Attempt:  3,
	})

	require.Error(t, err)
	require.Empty(t, queue.jobs)
	require.Len(t, queue.dlq, 1)
	require.Equal(t, domain.OrderStatusFailed, repo.orders[order.ID].Status)
	require.Equal(t, domain.OrderStatusFailed, cache.store[order.ID].Status)
	require.Equal(t, "failed", metrics.jobDuration[0].Status)
	require.Equal(t, "shipping", metrics.orderFailed[0].Stage)
}
