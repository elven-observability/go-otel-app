package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/elven-observability/go-otel-app/internal/domain"
	"github.com/elven-observability/go-otel-app/internal/ports"
)

func testTracer() trace.Tracer {
	return noop.NewTracerProvider().Tracer("test")
}

type fakeOrderRepository struct {
	mu         sync.Mutex
	orders     map[string]domain.Order
	createErr  error
	getErr     error
	updateErr  error
	updateLogs []statusUpdate
}

type statusUpdate struct {
	OrderID       string
	Status        domain.OrderStatus
	FailureReason *string
}

func newFakeOrderRepository() *fakeOrderRepository {
	return &fakeOrderRepository{orders: map[string]domain.Order{}}
}

func (f *fakeOrderRepository) Create(_ context.Context, order domain.Order) (domain.Order, error) {
	if f.createErr != nil {
		return domain.Order{}, f.createErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if order.CreatedAt.IsZero() {
		order.CreatedAt = time.Now().UTC()
	}
	if order.UpdatedAt.IsZero() {
		order.UpdatedAt = order.CreatedAt
	}
	f.orders[order.ID] = order
	return order, nil
}

func (f *fakeOrderRepository) GetByID(_ context.Context, id string) (domain.Order, error) {
	if f.getErr != nil {
		return domain.Order{}, f.getErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	order, ok := f.orders[id]
	if !ok {
		return domain.Order{}, domain.NewNotFoundError("order not found")
	}
	return order, nil
}

func (f *fakeOrderRepository) UpdateStatus(_ context.Context, id string, status domain.OrderStatus, failureReason *string) error {
	if f.updateErr != nil {
		return f.updateErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	order, ok := f.orders[id]
	if !ok {
		return domain.NewNotFoundError("order not found")
	}

	order.Status = status
	order.FailureReason = failureReason
	order.UpdatedAt = time.Now().UTC()
	f.orders[id] = order
	f.updateLogs = append(f.updateLogs, statusUpdate{
		OrderID:       id,
		Status:        status,
		FailureReason: failureReason,
	})
	return nil
}

func (f *fakeOrderRepository) Ping(context.Context) error {
	return nil
}

type fakeOrderCache struct {
	mu     sync.Mutex
	store  map[string]domain.Order
	setErr error
	getErr error
}

func newFakeOrderCache() *fakeOrderCache {
	return &fakeOrderCache{store: map[string]domain.Order{}}
}

func (f *fakeOrderCache) Set(_ context.Context, order domain.Order) error {
	if f.setErr != nil {
		return f.setErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.store[order.ID] = order
	return nil
}

func (f *fakeOrderCache) Get(_ context.Context, id string) (domain.Order, error) {
	if f.getErr != nil {
		return domain.Order{}, f.getErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	order, ok := f.store[id]
	if !ok {
		return domain.Order{}, domain.NewNotFoundError("order cache miss")
	}
	return order, nil
}

func (f *fakeOrderCache) Ping(context.Context) error {
	return nil
}

type fakeQueue struct {
	mu         sync.Mutex
	jobs       []domain.FulfillmentJob
	dlq        []domain.FulfillmentJob
	enqueueErr error
	dlqErr     error
}

func (f *fakeQueue) Enqueue(_ context.Context, job domain.FulfillmentJob) error {
	if f.enqueueErr != nil {
		return f.enqueueErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, job)
	return nil
}

func (f *fakeQueue) Dequeue(context.Context, time.Duration) (domain.FulfillmentJob, error) {
	return domain.FulfillmentJob{}, ports.ErrQueueEmpty
}

func (f *fakeQueue) SendToDLQ(_ context.Context, job domain.FulfillmentJob, _ string) error {
	if f.dlqErr != nil {
		return f.dlqErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.dlq = append(f.dlq, job)
	return nil
}

func (f *fakeQueue) Depth(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.jobs)), nil
}

func (f *fakeQueue) Ping(context.Context) error {
	return nil
}

type fakePaymentGateway struct {
	err error
}

func (f fakePaymentGateway) Authorize(context.Context, domain.Order, domain.Simulation) error {
	return f.err
}

type fakeInventoryGateway struct {
	err error
}

func (f fakeInventoryGateway) Reserve(context.Context, domain.Order, domain.Simulation) error {
	return f.err
}

type fakeShippingGateway struct {
	err error
}

func (f fakeShippingGateway) Book(context.Context, domain.Order, domain.FulfillmentJob) error {
	return f.err
}

type metricsCall struct {
	Status string
	Stage  string
	Type   string
}

type fakeMetrics struct {
	orderCreate []metricsCall
	orderFailed []metricsCall
	jobDuration []metricsCall
	orderCount  int
	queueDepth  int64
	active      int64
}

func (f *fakeMetrics) RecordOrderCreate(_ context.Context, _ time.Duration, status string) {
	f.orderCreate = append(f.orderCreate, metricsCall{Status: status})
}

func (f *fakeMetrics) IncrementOrderCreated(context.Context) {
	f.orderCount++
}

func (f *fakeMetrics) IncrementOrderFailed(_ context.Context, stage, errorType string) {
	f.orderFailed = append(f.orderFailed, metricsCall{Stage: stage, Type: errorType})
}

func (f *fakeMetrics) RecordJobDuration(_ context.Context, _ time.Duration, status string) {
	f.jobDuration = append(f.jobDuration, metricsCall{Status: status})
}

func (f *fakeMetrics) SetQueueDepth(value int64) {
	f.queueDepth = value
}

func (f *fakeMetrics) SetWorkerActive(value int64) {
	f.active = value
}

func assertDomainErrorType(err error, code string) bool {
	var domainErr *domain.Error
	return errors.As(err, &domainErr) && domainErr.Code == code
}
