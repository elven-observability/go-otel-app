package application

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/elven-observability/go-otel-app/internal/domain"
)

func TestCreateOrderSuccess(t *testing.T) {
	t.Parallel()

	repo := newFakeOrderRepository()
	cache := newFakeOrderCache()
	queue := &fakeQueue{}
	metrics := &fakeMetrics{}

	useCase := CreateOrderUseCase{
		Repo:      repo,
		Cache:     cache,
		Queue:     queue,
		Payment:   fakePaymentGateway{},
		Inventory: fakeInventoryGateway{},
		Metrics:   metrics,
		Tracer:    testTracer(),
	}

	order, err := useCase.Execute(context.Background(), domain.CreateOrderInput{
		CustomerID:  "cust-123",
		SKU:         "sku-observability-kit",
		Quantity:    2,
		AmountCents: 15990,
		Simulation: domain.Simulation{
			ShippingLatencyMS: 150,
			WorkerLatencyMS:   200,
		},
	})

	require.NoError(t, err)
	require.Equal(t, domain.OrderStatusQueued, order.Status)
	require.Len(t, queue.jobs, 1)
	require.Equal(t, 1, metrics.orderCount)
	require.Equal(t, "success", metrics.orderCreate[0].Status)
	require.Equal(t, domain.OrderStatusQueued, cache.store[order.ID].Status)
	require.Equal(t, domain.OrderStatusQueued, repo.orders[order.ID].Status)
	require.Equal(t, 1, queue.jobs[0].Attempt)
	require.EqualValues(t, 150, queue.jobs[0].ShippingLatencyMS)
	require.EqualValues(t, 200, queue.jobs[0].WorkerLatencyMS)
}

func TestCreateOrderPaymentFailure(t *testing.T) {
	t.Parallel()

	repo := newFakeOrderRepository()
	cache := newFakeOrderCache()
	queue := &fakeQueue{}
	metrics := &fakeMetrics{}

	useCase := CreateOrderUseCase{
		Repo:      repo,
		Cache:     cache,
		Queue:     queue,
		Payment:   fakePaymentGateway{err: domain.NewDependencyError("payment_failed", "payment timeout", "payment_dependency")},
		Inventory: fakeInventoryGateway{},
		Metrics:   metrics,
		Tracer:    testTracer(),
	}

	_, err := useCase.Execute(context.Background(), domain.CreateOrderInput{
		CustomerID:  "cust-123",
		SKU:         "sku-1",
		Quantity:    1,
		AmountCents: 1000,
	})

	require.Error(t, err)
	require.True(t, assertDomainErrorType(err, "payment_failed"))
	require.Len(t, queue.jobs, 0)
	require.Equal(t, domain.OrderStatusFailed, firstStoredOrder(repo).Status)
	require.Equal(t, "payment", metrics.orderFailed[0].Stage)
}

func TestCreateOrderInventoryFailure(t *testing.T) {
	t.Parallel()

	repo := newFakeOrderRepository()
	cache := newFakeOrderCache()
	queue := &fakeQueue{}
	metrics := &fakeMetrics{}

	useCase := CreateOrderUseCase{
		Repo:      repo,
		Cache:     cache,
		Queue:     queue,
		Payment:   fakePaymentGateway{},
		Inventory: fakeInventoryGateway{err: domain.NewDependencyError("inventory_failed", "inventory unavailable", "inventory_dependency")},
		Metrics:   metrics,
		Tracer:    testTracer(),
	}

	_, err := useCase.Execute(context.Background(), domain.CreateOrderInput{
		CustomerID:  "cust-456",
		SKU:         "sku-2",
		Quantity:    1,
		AmountCents: 2000,
	})

	require.Error(t, err)
	require.True(t, assertDomainErrorType(err, "inventory_failed"))
	require.Len(t, queue.jobs, 0)
	require.Equal(t, domain.OrderStatusFailed, firstStoredOrder(repo).Status)
	require.Equal(t, "inventory", metrics.orderFailed[0].Stage)
}

func TestCreateOrderPersistFailure(t *testing.T) {
	t.Parallel()

	repo := newFakeOrderRepository()
	repo.createErr = errors.New("database unavailable")
	cache := newFakeOrderCache()
	queue := &fakeQueue{}
	metrics := &fakeMetrics{}

	useCase := CreateOrderUseCase{
		Repo:      repo,
		Cache:     cache,
		Queue:     queue,
		Payment:   fakePaymentGateway{},
		Inventory: fakeInventoryGateway{},
		Metrics:   metrics,
		Tracer:    testTracer(),
	}

	_, err := useCase.Execute(context.Background(), domain.CreateOrderInput{
		CustomerID:  "cust-789",
		SKU:         "sku-3",
		Quantity:    1,
		AmountCents: 3000,
	})

	require.Error(t, err)
	require.True(t, assertDomainErrorType(err, "internal_error"))
	require.Len(t, repo.orders, 0)
	require.Len(t, queue.jobs, 0)
	require.Equal(t, "persist", metrics.orderFailed[0].Stage)
}

func TestCreateOrderEnqueueFailure(t *testing.T) {
	t.Parallel()

	repo := newFakeOrderRepository()
	cache := newFakeOrderCache()
	queue := &fakeQueue{enqueueErr: errors.New("queue unavailable")}
	metrics := &fakeMetrics{}

	useCase := CreateOrderUseCase{
		Repo:      repo,
		Cache:     cache,
		Queue:     queue,
		Payment:   fakePaymentGateway{},
		Inventory: fakeInventoryGateway{},
		Metrics:   metrics,
		Tracer:    testTracer(),
	}

	_, err := useCase.Execute(context.Background(), domain.CreateOrderInput{
		CustomerID:  "cust-999",
		SKU:         "sku-4",
		Quantity:    1,
		AmountCents: 4000,
	})

	require.Error(t, err)
	require.True(t, assertDomainErrorType(err, "internal_error"))
	require.Equal(t, domain.OrderStatusFailed, firstStoredOrder(repo).Status)
	require.Equal(t, domain.OrderStatusFailed, cache.store[firstStoredOrder(repo).ID].Status)
	require.Equal(t, "enqueue", metrics.orderFailed[0].Stage)
}

func TestCreateOrderDoesNotOverwriteFastFulfillment(t *testing.T) {
	t.Parallel()

	repo := newFakeOrderRepository()
	cache := newFakeOrderCache()
	queue := &immediateFulfillmentQueue{}
	metrics := &fakeMetrics{}
	worker := ProcessFulfillmentUseCase{
		Repo: repo, Cache: cache, Queue: queue, Metrics: metrics,
		Shipping: fakeShippingGateway{}, Tracer: testTracer(), MaxAttempts: 1,
	}
	queue.fulfill = worker.Execute
	create := CreateOrderUseCase{
		Repo: repo, Cache: cache, Queue: queue, Metrics: metrics,
		Payment: fakePaymentGateway{}, Inventory: fakeInventoryGateway{}, Tracer: testTracer(),
	}
	order, err := create.Execute(context.Background(), domain.CreateOrderInput{
		CustomerID: "synthetic-customer", SKU: "synthetic-item", Quantity: 1, AmountCents: 100,
	})

	require.NoError(t, err)
	require.Equal(t, domain.OrderStatusQueued, order.Status)
	require.Len(t, queue.jobs, 1)
	require.Len(t, repo.updateLogs, 2)
	require.Equal(t, []domain.OrderStatus{domain.OrderStatusQueued, domain.OrderStatusFulfilled},
		[]domain.OrderStatus{repo.updateLogs[0].Status, repo.updateLogs[1].Status})
	require.Equal(t, domain.OrderStatusFulfilled, repo.orders[order.ID].Status)
	require.Equal(t, domain.OrderStatusFulfilled, cache.store[order.ID].Status)
}

func TestCreateOrderQueuedCacheFailureDoesNotPublish(t *testing.T) {
	t.Parallel()

	repo := newFakeOrderRepository()
	cache := &queuedWriteFailingCache{newFakeOrderCache()}
	queue := &fakeQueue{}
	create := CreateOrderUseCase{
		Repo: repo, Cache: cache, Queue: queue, Metrics: &fakeMetrics{},
		Payment: fakePaymentGateway{}, Inventory: fakeInventoryGateway{}, Tracer: testTracer(),
	}
	_, err := create.Execute(context.Background(), domain.CreateOrderInput{
		CustomerID: "synthetic-customer", SKU: "synthetic-item", Quantity: 1, AmountCents: 100,
	})

	require.Error(t, err)
	require.Empty(t, queue.jobs)
	order := firstStoredOrder(repo)
	require.Equal(t, domain.OrderStatusFailed, order.Status)
	require.Equal(t, domain.OrderStatusFailed, cache.store[order.ID].Status)
}

type immediateFulfillmentQueue struct {
	fakeQueue
	fulfill func(context.Context, domain.FulfillmentJob) error
}

func (q *immediateFulfillmentQueue) Enqueue(ctx context.Context, job domain.FulfillmentJob) error {
	if err := q.fakeQueue.Enqueue(ctx, job); err != nil {
		return err
	}
	return q.fulfill(ctx, job)
}

type queuedWriteFailingCache struct {
	*fakeOrderCache
}

func (c *queuedWriteFailingCache) Set(ctx context.Context, order domain.Order) error {
	if order.Status == domain.OrderStatusQueued {
		return errors.New("queued cache write failed")
	}
	return c.fakeOrderCache.Set(ctx, order)
}

func firstStoredOrder(repo *fakeOrderRepository) domain.Order {
	for _, order := range repo.orders {
		return order
	}
	return domain.Order{}
}
