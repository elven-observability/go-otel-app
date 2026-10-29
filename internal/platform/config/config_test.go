package config

import "testing"

func TestInvalidTelemetryDoesNotBlockBusinessConfiguration(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "%invalid")
	if _, err := Load(); err != nil {
		t.Fatalf("telemetry blocked startup: %v", err)
	}
}
