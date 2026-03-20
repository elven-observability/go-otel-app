package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppName               string
	Environment           string
	ServiceVersion        string
	HTTPAddr              string
	PublicBaseURL         string
	RequestTimeout        time.Duration
	ShutdownTimeout       time.Duration
	WorkerPollTimeout     time.Duration
	WorkerMaxAttempts     int
	CacheTTL              time.Duration
	DatabaseURL           string
	RedisAddr             string
	RedisPassword         string
	RedisDB               int
	QueueName             string
	DLQName               string
	OTLPTracesEndpoint    string
	OTLPMetricsEndpoint   string
	OTLPHeaders           map[string]string
	DeploymentEnvironment string
}

func Load() (Config, error) {
	cfg := Config{
		AppName:               getEnv("OTEL_SERVICE_NAME", getEnv("APP_NAME", "go-otel-app")),
		Environment:           getEnv("APP_ENV", "local"),
		ServiceVersion:        getEnv("APP_VERSION", "1.0.0"),
		HTTPAddr:              getEnv("HTTP_ADDR", ":8080"),
		RequestTimeout:        getDurationEnv("REQUEST_TIMEOUT", 10*time.Second),
		ShutdownTimeout:       getDurationEnv("SHUTDOWN_TIMEOUT", 10*time.Second),
		WorkerPollTimeout:     getDurationEnv("WORKER_POLL_TIMEOUT", 2*time.Second),
		WorkerMaxAttempts:     getIntEnv("WORKER_MAX_ATTEMPTS", 3),
		CacheTTL:              getDurationEnv("CACHE_TTL", 5*time.Minute),
		DatabaseURL:           getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/go_golden_signals_demo?sslmode=disable"),
		RedisAddr:             getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:         getEnv("REDIS_PASSWORD", ""),
		RedisDB:               getIntEnv("REDIS_DB", 0),
		QueueName:             getEnv("REDIS_QUEUE_NAME", "fulfillment:jobs"),
		DLQName:               getEnv("REDIS_DLQ_NAME", "fulfillment:jobs:dlq"),
		OTLPTracesEndpoint:    getEnv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://localhost:4318/v1/traces"),
		OTLPMetricsEndpoint:   getEnv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://localhost:4318/v1/metrics"),
		OTLPHeaders:           parseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")),
		DeploymentEnvironment: getEnv("OTEL_DEPLOYMENT_ENVIRONMENT", getEnv("APP_ENV", "local")),
	}

	cfg.PublicBaseURL = getEnv("PUBLIC_BASE_URL", defaultPublicBaseURL(cfg.HTTPAddr))

	if _, err := url.Parse(cfg.DatabaseURL); err != nil {
		return Config{}, fmt.Errorf("invalid DATABASE_URL: %w", err)
	}

	if _, err := url.Parse(cfg.OTLPTracesEndpoint); err != nil {
		return Config{}, fmt.Errorf("invalid OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: %w", err)
	}

	if _, err := url.Parse(cfg.OTLPMetricsEndpoint); err != nil {
		return Config{}, fmt.Errorf("invalid OTEL_EXPORTER_OTLP_METRICS_ENDPOINT: %w", err)
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}

	return fallback
}

func getIntEnv(key string, fallback int) int {
	raw := getEnv(key, "")
	if raw == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}

	return value
}

func getDurationEnv(key string, fallback time.Duration) time.Duration {
	raw := getEnv(key, "")
	if raw == "" {
		return fallback
	}

	value, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}

	return value
}

func parseHeaders(raw string) map[string]string {
	headers := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return headers
	}

	for _, pair := range strings.Split(raw, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(pair), "=")
		if !found || key == "" {
			continue
		}

		decoded, err := url.QueryUnescape(value)
		if err != nil {
			decoded = value
		}
		headers[key] = decoded
	}

	return headers
}

func defaultPublicBaseURL(httpAddr string) string {
	host := strings.TrimPrefix(httpAddr, ":")
	if host == "" {
		host = "8080"
	}

	if strings.HasPrefix(httpAddr, ":") {
		return "http://localhost:" + host
	}

	return "http://" + strings.TrimPrefix(httpAddr, "http://")
}
