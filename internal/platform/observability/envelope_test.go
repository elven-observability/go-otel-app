package observability

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func envelopeFixture() tracetest.SpanStub {
	state, _ := trace.ParseTraceState("tenant=opaque,other=value")
	ctx := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled, TraceState: state, Remote: true})
	return tracetest.SpanStub{
		Name: "operation", SpanContext: ctx, Parent: ctx, SpanKind: trace.SpanKindServer,
		StartTime: time.Unix(100, 2), EndTime: time.Unix(100, 3),
		Attributes:           []attribute.KeyValue{attribute.String("key", "value"), attribute.StringSlice("array", []string{"one", "two"})},
		Resource:             resource.NewWithAttributes("resource-schema", attribute.String("service.name", "test")),
		InstrumentationScope: instrumentation.Scope{Name: "scope", Version: "version", SchemaURL: "scope-schema", Attributes: attribute.NewSet(attribute.String("scope.key", "value"))},
		Events:               []sdktrace.Event{{Name: "event", Time: time.Unix(100, 2), Attributes: []attribute.KeyValue{attribute.String("event.key", "value")}, DroppedAttributeCount: 2}},
		Links:                []sdktrace.Link{{SpanContext: ctx, Attributes: []attribute.KeyValue{attribute.String("link.key", "value")}, DroppedAttributeCount: 3}},
		Status:               sdktrace.Status{Code: codes.Error, Description: "description"},
		DroppedAttributes:    1, DroppedEvents: 2, DroppedLinks: 3, ChildSpanCount: 4,
	}
}

func TestEnvelopeAllPublicGettersAndDetachedContainers(t *testing.T) {
	fixture := envelopeFixture()
	snapshot, size, err := detachedSpan(fixture.Snapshot())
	if err != nil || size > spanEnvelopeLimit {
		t.Fatalf("size=%d error=%v", size, err)
	}
	if snapshot.Name() != fixture.Name || !snapshot.SpanContext().Equal(fixture.SpanContext) || !snapshot.Parent().Equal(fixture.Parent) || snapshot.SpanKind() != fixture.SpanKind || !snapshot.StartTime().Equal(fixture.StartTime) || !snapshot.EndTime().Equal(fixture.EndTime) || snapshot.Status() != fixture.Status || snapshot.DroppedAttributes() != 1 || snapshot.DroppedEvents() != 2 || snapshot.DroppedLinks() != 3 || snapshot.ChildSpanCount() != 4 {
		t.Fatal("scalar or context getter changed")
	}
	scope := snapshot.InstrumentationScope()
	if snapshot.Resource() == fixture.Resource || snapshot.Resource().SchemaURL() != "resource-schema" || scope.SchemaURL != "scope-schema" || snapshot.InstrumentationLibrary().Name != "scope" || scope.Attributes.Len() != 1 {
		t.Fatal("resource or scope contract changed")
	}
	fixture.Attributes[0] = attribute.String("changed", "changed")
	fixture.Events[0].Attributes[0] = attribute.String("changed", "changed")
	fixture.Links[0].Attributes[0] = attribute.String("changed", "changed")
	if snapshot.Attributes()[0].Key != "key" || snapshot.Events()[0].Attributes[0].Key != "event.key" || snapshot.Links()[0].Attributes[0].Key != "link.key" || snapshot.Events()[0].DroppedAttributeCount != 2 || snapshot.Links()[0].DroppedAttributeCount != 3 || !snapshot.Links()[0].SpanContext.Equal(fixture.SpanContext) {
		t.Fatal("snapshot retained a caller container")
	}
	t.Logf("aggregate_data_charge=%d cap=%d", size, spanEnvelopeLimit)
}

func TestEnvelopeValueTypesAndBudget(t *testing.T) {
	values := []attribute.Value{
		{}, attribute.BoolValue(true), attribute.Int64Value(2), attribute.Float64Value(3), attribute.StringValue("value"),
		attribute.BoolSliceValue([]bool{true, false}), attribute.Int64SliceValue([]int64{1, 2}), attribute.Float64SliceValue([]float64{1, 2}), attribute.StringSliceValue([]string{"one", "two"}),
		attribute.ByteSliceValue([]byte{0, 255}), attribute.SliceValue(attribute.StringValue("nested")), attribute.MapValue(attribute.String("key", "nested")),
	}
	for _, value := range values {
		b := envelopeBudget{left: spanEnvelopeLimit}
		copied, err := b.value(value)
		if err != nil || copied.Type() != value.Type() || copied.Emit() != value.Emit() {
			t.Fatalf("type %v error=%v", value.Type(), err)
		}
	}
	b := envelopeBudget{left: spanEnvelopeLimit}
	if _, err := b.value(attribute.StringSliceValue(make([]string, 4096))); err == nil {
		t.Fatal("unbounded array accepted")
	}
	nested := attribute.StringValue("end")
	for i := 0; i < maxValueDepth+2; i++ {
		nested = attribute.SliceValue(nested)
	}
	if _, err := b.value(nested); err == nil {
		t.Fatal("unbounded nesting accepted")
	}
}

func TestEnvelopeRejectsFullMetadataBeforeQueue(t *testing.T) {
	large := strings.Repeat("x", spanEnvelopeLimit+1)
	cases := map[string]func(*tracetest.SpanStub){
		"name":            func(s *tracetest.SpanStub) { s.Name = large },
		"key":             func(s *tracetest.SpanStub) { s.Attributes = []attribute.KeyValue{attribute.String(large, "v")} },
		"value":           func(s *tracetest.SpanStub) { s.Attributes = []attribute.KeyValue{attribute.String("key", large)} },
		"resource":        func(s *tracetest.SpanStub) { s.Resource = resource.NewSchemaless(attribute.String("resource", large)) },
		"resource-schema": func(s *tracetest.SpanStub) { s.Resource = resource.NewWithAttributes(large) },
		"scope":           func(s *tracetest.SpanStub) { s.InstrumentationScope.Name = large },
		"scope-version":   func(s *tracetest.SpanStub) { s.InstrumentationScope.Version = large },
		"scope-schema":    func(s *tracetest.SpanStub) { s.InstrumentationScope.SchemaURL = large },
		"scope-attrs": func(s *tracetest.SpanStub) {
			s.InstrumentationScope.Attributes = attribute.NewSet(attribute.String("scope", large))
		},
		"event": func(s *tracetest.SpanStub) { s.Events[0].Name = large },
		"event-attrs": func(s *tracetest.SpanStub) {
			s.Events[0].Attributes = []attribute.KeyValue{attribute.String("event", large)}
		},
		"link-attrs": func(s *tracetest.SpanStub) {
			s.Links[0].Attributes = []attribute.KeyValue{attribute.String("link", large)}
		},
		"status": func(s *tracetest.SpanStub) { s.Status.Description = large },
		"aggregate": func(s *tracetest.SpanStub) {
			s.Attributes = make([]attribute.KeyValue, 16)
			for i := range s.Attributes {
				s.Attributes[i] = attribute.String("key", strings.Repeat("x", 2048))
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := envelopeFixture()
			change(&fixture)
			sink := tracetest.NewInMemoryExporter()
			p, err := newEnvelopeProcessor(sdktrace.NewSimpleSpanProcessor(sink))
			if err != nil {
				t.Fatal(err)
			}
			p.OnEnd(fixture.Snapshot())
			if p.rejected.Load() != 1 || len(sink.GetSpans()) != 0 {
				t.Fatal("rejected input reached downstream processor")
			}
			_ = p.Shutdown(context.Background())
		})
	}
	if validateSnapshotSDK("1.48.0") == nil {
		t.Fatal("unsupported snapshot constructor contract accepted")
	}
}

type blockedEnvelopeExporter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *blockedEnvelopeExporter) ExportSpans(ctx context.Context, _ []sdktrace.ReadOnlySpan) error {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*blockedEnvelopeExporter) Shutdown(context.Context) error { return nil }

func enqueueBorrowedParents(p sdktrace.SpanProcessor, truncate bool) {
	limits := sdktrace.NewSpanLimits()
	limits.AttributeValueLengthLimit = 256
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(p), sdktrace.WithRawSpanLimits(limits))
	tracer := provider.Tracer("retainer-probe")
	for i := 0; i < 8; i++ {
		parent := strings.Repeat("p", 1<<20)
		value := parent[len(parent)-128:]
		if truncate {
			value = parent
		}
		_, span := tracer.Start(context.Background(), "operation", trace.WithAttributes(attribute.String("value", value)))
		span.End()
	}
}

func retainedQueueBytes(t *testing.T, bounded, truncate bool) uint64 {
	t.Helper()
	exporter := &blockedEnvelopeExporter{started: make(chan struct{}), release: make(chan struct{})}
	bsp := sdktrace.NewBatchSpanProcessor(exporter, sdktrace.WithMaxQueueSize(16), sdktrace.WithMaxExportBatchSize(1), sdktrace.WithBatchTimeout(time.Hour), sdktrace.WithExportTimeout(30*time.Second))
	var p sdktrace.SpanProcessor = bsp
	if bounded {
		var err error
		p, err = newEnvelopeProcessor(bsp)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, initial := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(p)).Tracer("blocker").Start(context.Background(), "initial")
	initial.End()
	select {
	case <-exporter.started:
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not block")
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	enqueueBorrowedParents(p, truncate)
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	close(exporter.release)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if after.HeapAlloc < before.HeapAlloc {
		return 0
	}
	return after.HeapAlloc - before.HeapAlloc
}

func TestEnvelopeActualBlockedBSPDetachesBackingStorage(t *testing.T) {
	for _, truncate := range []bool{false, true} {
		raw := retainedQueueBytes(t, false, truncate)
		detached := retainedQueueBytes(t, true, truncate)
		t.Logf("8x1MiB truncate=%v raw_retained=%d detached_retained=%d shared_host_noisy=true", truncate, raw, detached)
		if raw < 6<<20 || detached > 2<<20 {
			t.Fatal("physical backing retention control failed")
		}
	}
}

func metadataSnapshot(t *testing.T, bounded bool) sdktrace.ReadOnlySpan {
	t.Helper()
	parent := strings.Repeat("p", 1<<20)
	tail := func() string { return parent[len(parent)-128:] }
	value := envelopeFixture()
	value.Name = tail()
	value.Attributes = []attribute.KeyValue{
		attribute.String(tail(), tail()),
		attribute.StringSlice("array", []string{tail()}),
		{Key: "nested", Value: attribute.SliceValue(attribute.StringValue(tail()))},
		{Key: "map", Value: attribute.MapValue(attribute.String(tail(), tail()))},
	}
	value.Resource = resource.NewWithAttributes(tail(), attribute.String(tail(), tail()))
	value.InstrumentationScope = instrumentation.Scope{
		Name: tail(), Version: tail(), SchemaURL: tail(),
		Attributes: attribute.NewSet(attribute.String(tail(), tail())),
	}
	value.Events[0].Name = tail()
	value.Events[0].Attributes = []attribute.KeyValue{attribute.String(tail(), tail())}
	value.Links[0].Attributes = []attribute.KeyValue{attribute.String(tail(), tail())}
	value.Status.Description = tail()
	state, err := (trace.TraceState{}).Insert(tail(), tail())
	if err != nil {
		t.Fatal(err)
	}
	value.SpanContext = value.SpanContext.WithTraceState(state)
	value.Parent = value.Parent.WithTraceState(state)
	value.Links[0].SpanContext = value.SpanContext
	if !bounded {
		return value.Snapshot()
	}
	copied, _, err := detachedSpan(value.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	return copied
}

func TestEnvelopeAllMetadataBackingIsDetached(t *testing.T) {
	retained := func(bounded bool) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		snapshot := metadataSnapshot(t, bounded)
		runtime.GC()
		runtime.GC()
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(snapshot)
		if after.HeapAlloc < before.HeapAlloc {
			return 0
		}
		return after.HeapAlloc - before.HeapAlloc
	}
	raw, copied := retained(false), retained(true)
	t.Logf("all-metadata shared1MiB-parent raw_retained=%d detached_retained=%d shared_host_noisy=true", raw, copied)
	if raw < 512<<10 || copied > 512<<10 {
		t.Fatal("metadata backing control failed")
	}
}
