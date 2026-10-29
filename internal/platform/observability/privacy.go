package observability

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Keep only bounded operational dimensions. Payloads, SQL, URLs, headers and
// exception text are deliberately not telemetry in this reference application.
func safeAttribute(a attribute.KeyValue) bool {
	switch string(a.Key) {
	case "http.request.method", "http.response.status_code", "http.route", "network.protocol.version", "url.scheme", "db.system.name", "db.system", "db.operation.name", "db.operation", "status", "stage", "error.type", "otel.metric.overflow", "go.memory.type", "go.goroutine.state", "state", "type":
		return len(a.Value.Emit()) <= 256
	}
	return false
}

type privateSpan struct{ sdktrace.ReadOnlySpan }

func (s privateSpan) Attributes() []attribute.KeyValue {
	result := make([]attribute.KeyValue, 0, len(s.ReadOnlySpan.Attributes()))
	for _, a := range s.ReadOnlySpan.Attributes() {
		if safeAttribute(a) {
			result = append(result, a)
		}
	}
	return result
}

func (s privateSpan) Events() []sdktrace.Event { return nil }
func (s privateSpan) Links() []sdktrace.Link   { return nil }
func (s privateSpan) Status() sdktrace.Status {
	return sdktrace.Status{Code: s.ReadOnlySpan.Status().Code}
}

type privateExporter struct{ sdktrace.SpanExporter }

func (e privateExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	clean := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, span := range spans {
		clean[i] = privateSpan{span}
	}
	return e.SpanExporter.ExportSpans(ctx, clean)
}
