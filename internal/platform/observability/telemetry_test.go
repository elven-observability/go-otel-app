package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/elven-observability/go-otel-app/internal/platform/config"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	collectormetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func testConfig(endpoint string) config.Config {
	return config.Config{AppName: "audit-reference", ServiceVersion: "test", DeploymentEnvironment: "test", OTLPTracesEndpoint: endpoint + "/v1/traces", OTLPMetricsEndpoint: endpoint + "/v1/metrics"}
}

func TestHealthyExportPrivacyAndContext(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string][][]byte{}
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[r.URL.Path] = append(bodies[r.URL.Path], body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	telemetry, err := New(context.Background(), testConfig(collector.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer telemetry.Shutdown(context.Background())
	ctx, root := telemetry.Tracer.Start(context.Background(), "order.create")
	_, child := telemetry.Tracer.Start(ctx, "order.create.persist", trace.WithAttributes(attribute.String("db.statement", "select 'audit-secret-PII'"), attribute.String("http.route", "/api/v1/orders/{id}")))
	child.RecordError(errors.New("audit-secret-PII"))
	child.SetStatus(codes.Error, "audit-secret-PII")
	child.End()
	root.End()
	telemetry.Metrics.IncrementOrderCreated(ctx)
	if err := telemetry.traceProvider.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.meterProvider.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies["/v1/traces"]) != 1 || len(bodies["/v1/metrics"]) != 1 {
		t.Fatalf("missing signals: %v", bodies)
	}
	var traces collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(bodies["/v1/traces"][0], &traces); err != nil {
		t.Fatal(err)
	}
	spans := traces.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 2 || string(spans[0].ParentSpanId) != string(spans[1].SpanId) {
		t.Fatal("lost parent context")
	}
	if strings.Contains(traces.String(), "audit-secret-PII") {
		t.Fatal("private data exported")
	}
	var metrics collectormetric.ExportMetricsServiceRequest
	if err := proto.Unmarshal(bodies["/v1/metrics"][0], &metrics); err != nil || len(metrics.ResourceMetrics) == 0 {
		t.Fatalf("invalid metrics: %v", err)
	}
	t.Logf("trace_bytes=%d metric_bytes=%d", len(bodies["/v1/traces"][0]), len(bodies["/v1/metrics"][0]))
}

func TestDisabledAndInvalidEndpoint(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	telemetry, err := New(context.Background(), testConfig("http://invalid-external.example"))
	if err != nil {
		t.Fatal(err)
	}
	_, span := telemetry.Tracer.Start(context.Background(), "disabled")
	if span.IsRecording() {
		t.Fatal("disable ignored")
	}
	telemetry.Metrics.IncrementOrderCreated(context.Background())
	if err := telemetry.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTEL_SDK_DISABLED", "false")
	for _, endpoint := range []string{"%", "http://collector.example", "https://user:secret@collector.example", "https://collector.example?token=secret"} {
		if _, err := New(context.Background(), testConfig(endpoint)); err == nil {
			t.Fatalf("accepted %q", endpoint)
		}
	}
}

func TestBackpressureIsBoundedAndBusinessContinues(t *testing.T) {
	var requests atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer collector.Close()
	telemetry, err := New(context.Background(), testConfig(collector.URL))
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	completed := 0
	for i := 0; i < 20000; i++ {
		_, span := telemetry.Tracer.Start(context.Background(), "order.create", trace.WithAttributes(attribute.String("payload", strings.Repeat("x", 4096))))
		span.End()
		completed++
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if completed != 20000 {
		t.Fatal("business work interrupted")
	}
	if after.HeapAlloc > before.HeapAlloc+64<<20 {
		t.Fatal("retained heap exceeded coarse leak guard")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_ = telemetry.Shutdown(ctx)
	if requests.Load() > 100 {
		t.Fatal("unbounded retry/recursion")
	}
	t.Logf("shared-host noisy: completed=%d elapsed=%s retained_heap_delta=%d requests=%d goroutines=%d", completed, time.Since(started), int64(after.HeapAlloc)-int64(before.HeapAlloc), requests.Load(), runtime.NumGoroutine())
}

func TestMetricCardinalityAndPrivacy(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer collector.Close()
	telemetry, err := New(context.Background(), testConfig(collector.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer telemetry.Shutdown(context.Background())
	counter, _ := telemetry.meterProvider.Meter("test").Int64Counter("audit.requests")
	for i := 0; i < 500; i++ {
		counter.Add(context.Background(), 1, metric.WithAttributes(attribute.String("http.route", fmt.Sprint(i)), attribute.String("customer.email", "audit-secret-PII")))
	}
	w := httptest.NewRecorder()
	telemetry.PrometheusHandler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	count := 0
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if strings.HasPrefix(line, "audit_requests_total{") {
			count++
		}
	}
	if count == 0 || count > 128 {
		t.Fatalf("cardinality=%d", count)
	}
	if strings.Contains(w.Body.String(), "audit-secret-PII") {
		t.Fatal("private metric dimension exported")
	}
	t.Logf("bounded_metric_series=%d", count)
}

func TestUntrustedTLSCollectorDoesNotDisableVerification(t *testing.T) {
	var received atomic.Int64
	collector := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received.Add(1) }))
	defer collector.Close()
	telemetry, err := New(context.Background(), testConfig(collector.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer telemetry.Shutdown(context.Background())
	_, span := telemetry.Tracer.Start(context.Background(), "order.create")
	span.End()
	if err := telemetry.traceProvider.ForceFlush(context.Background()); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	if received.Load() != 0 {
		t.Fatal("private data sent to untrusted TLS collector")
	}
}

func TestStalledCollectorHasBoundedExport(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(2500 * time.Millisecond) }))
	defer collector.Close()
	telemetry, err := New(context.Background(), testConfig(collector.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer telemetry.Shutdown(context.Background())
	_, span := telemetry.Tracer.Start(context.Background(), "order.create")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := telemetry.traceProvider.ForceFlush(ctx); err == nil {
		t.Fatal("stalled export unexpectedly succeeded")
	}
}

func BenchmarkTelemetryBackpressure(b *testing.B) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer collector.Close()
	telemetry, err := New(context.Background(), testConfig(collector.URL))
	if err != nil {
		b.Fatal(err)
	}
	defer telemetry.Shutdown(context.Background())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, span := telemetry.Tracer.Start(context.Background(), "order.create")
		span.End()
	}
}
