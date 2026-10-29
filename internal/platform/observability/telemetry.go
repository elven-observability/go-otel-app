package observability

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/elven-observability/go-otel-app/internal/platform/config"
)

type Telemetry struct {
	Tracer            trace.Tracer
	Metrics           *BusinessMetrics
	PrometheusHandler http.Handler
	traceProvider     *sdktrace.TracerProvider
	meterProvider     *sdkmetric.MeterProvider
}

type BusinessMetrics struct {
	orderCreateDuration metric.Float64Histogram
	orderCreated        metric.Int64Counter
	orderFailed         metric.Int64Counter
	jobDuration         metric.Float64Histogram
	queueDepthGauge     metric.Int64ObservableGauge
	workerActiveGauge   metric.Int64ObservableGauge
	queueDepthValue     atomic.Int64
	workerActiveValue   atomic.Int64
}

func New(ctx context.Context, cfg config.Config) (*Telemetry, error) {
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return Disabled(cfg.AppName), nil
	}
	for _, endpoint := range []string{cfg.OTLPTracesEndpoint, cfg.OTLPMetricsEndpoint} {
		if !safeEndpoint(endpoint) {
			return nil, errors.New("telemetry endpoint requires HTTPS or loopback HTTP, without userinfo or query")
		}
	}
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			attribute.String("service.name", cfg.AppName),
			attribute.String("service.version", cfg.ServiceVersion),
			attribute.String("deployment.environment", cfg.DeploymentEnvironment),
		),
	)
	if err != nil {
		return nil, err
	}

	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(cfg.OTLPTracesEndpoint),
		otlptracehttp.WithHeaders(cfg.OTLPHeaders),
		otlptracehttp.WithTimeout(2*time.Second),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: true, InitialInterval: 100 * time.Millisecond, MaxInterval: 500 * time.Millisecond, MaxElapsedTime: 2 * time.Second}),
	)
	if err != nil {
		return nil, err
	}

	metricExporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpointURL(cfg.OTLPMetricsEndpoint),
		otlpmetrichttp.WithHeaders(cfg.OTLPHeaders),
		otlpmetrichttp.WithTimeout(2*time.Second),
		otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: true, InitialInterval: 100 * time.Millisecond, MaxInterval: 500 * time.Millisecond, MaxElapsedTime: 2 * time.Second}),
	)
	if err != nil {
		_ = traceExporter.Shutdown(ctx)
		return nil, err
	}

	promRegistry := promclient.NewRegistry()
	promExporter, err := otelprom.New(
		otelprom.WithRegisterer(promRegistry),
		otelprom.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithSuffixes),
	)
	if err != nil {
		_ = metricExporter.Shutdown(ctx)
		_ = traceExporter.Shutdown(ctx)
		return nil, err
	}

	traceProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(privateExporter{traceExporter}, sdktrace.WithMaxQueueSize(256), sdktrace.WithMaxExportBatchSize(64), sdktrace.WithExportTimeout(2*time.Second)),
		sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{AttributeValueLengthLimit: 256, AttributeCountLimit: 32, EventCountLimit: 8, LinkCountLimit: 8, AttributePerEventCountLimit: 8, AttributePerLinkCountLimit: 8}),
		sdktrace.WithResource(res),
	)

	metricReader := sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(10*time.Second), sdkmetric.WithTimeout(2*time.Second))
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(metricReader),
		sdkmetric.WithReader(promExporter),
		sdkmetric.WithCardinalityLimit(128),
		sdkmetric.WithView(sdkmetric.NewView(sdkmetric.Instrument{Name: "*"}, sdkmetric.Stream{AttributeFilter: safeAttribute})),
	)

	if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
		_ = meterProvider.Shutdown(ctx)
		_ = traceProvider.Shutdown(ctx)
		return nil, err
	}

	meter := meterProvider.Meter(cfg.AppName)
	businessMetrics, err := newBusinessMetrics(meter)
	if err != nil {
		_ = meterProvider.Shutdown(ctx)
		_ = traceProvider.Shutdown(ctx)
		return nil, err
	}
	otel.SetTracerProvider(traceProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return &Telemetry{
		Tracer:            traceProvider.Tracer(cfg.AppName),
		Metrics:           businessMetrics,
		PrometheusHandler: promhttp.HandlerFor(promRegistry, promhttp.HandlerOpts{EnableOpenMetrics: true}),
		traceProvider:     traceProvider,
		meterProvider:     meterProvider,
	}, nil
}

func (t *Telemetry) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var err error
	if t.meterProvider != nil {
		err = t.meterProvider.Shutdown(ctx)
	}
	if t.traceProvider != nil {
		err = errors.Join(err, t.traceProvider.Shutdown(ctx))
	}
	return err
}

func Disabled(name string) *Telemetry {
	tp, mp := tracenoop.NewTracerProvider(), metricnoop.NewMeterProvider()
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	metrics, _ := newBusinessMetrics(mp.Meter(name))
	return &Telemetry{Tracer: tp.Tracer(name), Metrics: metrics, PrometheusHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
}

func safeEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))
}

func newBusinessMetrics(meter metric.Meter) (*BusinessMetrics, error) {
	orderCreateDuration, err := meter.Float64Histogram(
		"app.order.create.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of the order creation use case."),
	)
	if err != nil {
		return nil, err
	}

	orderCreated, err := meter.Int64Counter(
		"app.order.created",
		metric.WithUnit("{order}"),
		metric.WithDescription("Number of orders created successfully."),
	)
	if err != nil {
		return nil, err
	}

	orderFailed, err := meter.Int64Counter(
		"app.order.failed",
		metric.WithUnit("{order}"),
		metric.WithDescription("Number of order failures by stage."),
	)
	if err != nil {
		return nil, err
	}

	jobDuration, err := meter.Float64Histogram(
		"app.fulfillment.job.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of fulfillment worker jobs."),
	)
	if err != nil {
		return nil, err
	}

	queueDepthGauge, err := meter.Int64ObservableGauge(
		"app.fulfillment.queue.depth",
		metric.WithUnit("{job}"),
		metric.WithDescription("Current number of jobs waiting in the fulfillment queue."),
	)
	if err != nil {
		return nil, err
	}

	workerActiveGauge, err := meter.Int64ObservableGauge(
		"app.fulfillment.worker.active",
		metric.WithUnit("{worker}"),
		metric.WithDescription("Current number of active fulfillment workers."),
	)
	if err != nil {
		return nil, err
	}

	metrics := &BusinessMetrics{
		orderCreateDuration: orderCreateDuration,
		orderCreated:        orderCreated,
		orderFailed:         orderFailed,
		jobDuration:         jobDuration,
		queueDepthGauge:     queueDepthGauge,
		workerActiveGauge:   workerActiveGauge,
	}

	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		observer.ObserveInt64(metrics.queueDepthGauge, metrics.queueDepthValue.Load())
		observer.ObserveInt64(metrics.workerActiveGauge, metrics.workerActiveValue.Load())
		return nil
	}, queueDepthGauge, workerActiveGauge)
	if err != nil {
		return nil, err
	}

	return metrics, nil
}

func (m *BusinessMetrics) RecordOrderCreate(ctx context.Context, duration time.Duration, status string) {
	m.orderCreateDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attribute.String("status", status)))
}

func (m *BusinessMetrics) IncrementOrderCreated(ctx context.Context) {
	m.orderCreated.Add(ctx, 1)
}

func (m *BusinessMetrics) IncrementOrderFailed(ctx context.Context, stage, errorType string) {
	m.orderFailed.Add(ctx, 1, metric.WithAttributes(
		attribute.String("stage", stage),
		attribute.String("error.type", errorType),
	))
}

func (m *BusinessMetrics) RecordJobDuration(ctx context.Context, duration time.Duration, status string) {
	m.jobDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attribute.String("status", status)))
}

func (m *BusinessMetrics) SetQueueDepth(value int64) {
	m.queueDepthValue.Store(value)
}

func (m *BusinessMetrics) SetWorkerActive(value int64) {
	m.workerActiveValue.Store(value)
}
