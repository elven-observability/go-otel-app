package application

import (
	"context"
	"testing"

	"github.com/elven-observability/go-otel-app/internal/domain"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestWorkerContinuesQueueTrace(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	tracer := provider.Tracer("test")
	ctx, parent := tracer.Start(context.Background(), "request")
	repo := newFakeOrderRepository()
	order := domain.Order{ID: "order-context"}
	repo.orders[order.ID] = order
	queue := &fakeQueue{}
	create := CreateOrderUseCase{Repo: repo, Cache: newFakeOrderCache(), Queue: queue, Tracer: tracer}
	_, err := create.enqueueFulfillment(ctx, order, domain.Simulation{})
	require.NoError(t, err)
	require.Len(t, queue.jobs, 1)
	require.Len(t, queue.jobs[0].TraceParent, 55)
	worker := ProcessFulfillmentUseCase{Repo: repo, Cache: newFakeOrderCache(), Queue: queue, Shipping: fakeShippingGateway{}, Metrics: &fakeMetrics{}, Tracer: tracer}
	require.NoError(t, worker.Execute(context.Background(), queue.jobs[0]))
	parent.End()
	found := false
	for _, span := range recorder.Ended() {
		if span.Name() == "worker.fulfillment.process" {
			require.Equal(t, parent.SpanContext().TraceID(), span.SpanContext().TraceID())
			require.True(t, span.Parent().IsRemote())
			found = true
		}
	}
	require.True(t, found)
}
