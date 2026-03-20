//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/elven-observability/go-otel-app/internal/domain"
	"github.com/elven-observability/go-otel-app/internal/platform/bootstrap"
	"github.com/elven-observability/go-otel-app/internal/platform/config"
)

func TestHealthyOrderFlowAndMetrics(t *testing.T) {
	env := newIntegrationEnv(t, 3)

	response := env.createOrder(t, domain.CreateOrderInput{
		CustomerID:  "cust-otel-1",
		SKU:         "sku-golden-demo",
		Quantity:    2,
		AmountCents: 12990,
		Simulation: domain.Simulation{
			PaymentLatencyMS:   25,
			InventoryLatencyMS: 30,
			ShippingLatencyMS:  40,
			WorkerLatencyMS:    50,
		},
	})

	require.NotEmpty(t, response.TraceID)
	require.Equal(t, domain.OrderStatusQueued, response.Order.Status)

	finalOrder := env.waitForOrderStatus(t, response.Order.ID, domain.OrderStatusFulfilled)
	require.Equal(t, domain.OrderStatusFulfilled, finalOrder.Status)

	metricsBody := env.getMetrics(t)
	require.Contains(t, metricsBody, "app_order_create_duration_seconds")
	require.Contains(t, metricsBody, "app_fulfillment_job_duration_seconds")
	require.Contains(t, metricsBody, "http_server_request_duration_seconds")
	require.Contains(t, metricsBody, "db_client_operation_duration_seconds")
}

func TestShippingFailureGoesToDLQ(t *testing.T) {
	env := newIntegrationEnv(t, 2)

	response := env.createOrder(t, domain.CreateOrderInput{
		CustomerID:  "cust-otel-2",
		SKU:         "sku-dlq-demo",
		Quantity:    1,
		AmountCents: 4990,
		Simulation: domain.Simulation{
			FailShipping: true,
		},
	})

	require.NotEmpty(t, response.TraceID)

	finalOrder := env.waitForOrderStatus(t, response.Order.ID, domain.OrderStatusFailed)
	require.Equal(t, domain.OrderStatusFailed, finalOrder.Status)
	require.NotNil(t, finalOrder.FailureReason)
	require.Contains(t, *finalOrder.FailureReason, "shipping simulator forced failure")

	dlqDepth := env.redisClient.LLen(context.Background(), env.cfg.DLQName).Val()
	require.EqualValues(t, 1, dlqDepth)
}

type integrationEnv struct {
	cfg         config.Config
	baseURL     string
	app         *bootstrap.App
	cancel      context.CancelFunc
	redisClient *goredis.Client
	postgres    testcontainers.Container
	redis       testcontainers.Container
	otlpServer  *httptest.Server
}

type createOrderResponse struct {
	TraceID string       `json:"trace_id"`
	Order   domain.Order `json:"order"`
}

type getOrderResponse struct {
	TraceID string       `json:"trace_id"`
	Order   domain.Order `json:"order"`
}

func newIntegrationEnv(t *testing.T, workerMaxAttempts int) *integrationEnv {
	t.Helper()
	t.Setenv("OTEL_SEMCONV_STABILITY_OPT_IN", "database")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	otlpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(otlpServer.Close)

	postgresContainer, databaseURL := startPostgresContainer(t, ctx)
	redisContainer, redisAddr := startRedisContainer(t, ctx)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	httpAddr := listener.Addr().String()
	cfg := config.Config{
		AppName:               "go-otel-app-test",
		Environment:           "test",
		ServiceVersion:        "1.0.0-test",
		HTTPAddr:              httpAddr,
		PublicBaseURL:         "http://" + httpAddr,
		RequestTimeout:        5 * time.Second,
		ShutdownTimeout:       5 * time.Second,
		WorkerPollTimeout:     100 * time.Millisecond,
		WorkerMaxAttempts:     workerMaxAttempts,
		CacheTTL:              time.Minute,
		DatabaseURL:           databaseURL,
		RedisAddr:             redisAddr,
		RedisPassword:         "",
		RedisDB:               0,
		QueueName:             "fulfillment:jobs",
		DLQName:               "fulfillment:jobs:dlq",
		OTLPTracesEndpoint:    otlpServer.URL + "/v1/traces",
		OTLPMetricsEndpoint:   otlpServer.URL + "/v1/metrics",
		OTLPHeaders:           map[string]string{},
		DeploymentEnvironment: "test",
	}

	app, err := bootstrap.New(ctx, cfg)
	require.NoError(t, err)

	serverErrCh := make(chan error, 1)
	go func() {
		err := app.Server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
			return
		}
		serverErrCh <- nil
	}()

	workerErrCh := make(chan error, 1)
	go func() {
		err := app.Worker.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			workerErrCh <- err
			return
		}
		workerErrCh <- nil
	}()

	waitForReady(t, "http://"+httpAddr)

	redisClient := goredis.NewClient(&goredis.Options{Addr: redisAddr})

	env := &integrationEnv{
		cfg:         cfg,
		baseURL:     "http://" + httpAddr,
		app:         app,
		cancel:      cancel,
		redisClient: redisClient,
		postgres:    postgresContainer,
		redis:       redisContainer,
		otlpServer:  otlpServer,
	}

	t.Cleanup(func() {
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer shutdownCancel()

		_ = app.Server.Shutdown(shutdownCtx)
		_ = app.Shutdown(shutdownCtx)
		_ = redisClient.Close()
		_ = postgresContainer.Terminate(context.Background())
		_ = redisContainer.Terminate(context.Background())

		require.NoError(t, <-serverErrCh)
		require.NoError(t, <-workerErrCh)
	})

	return env
}

func (e *integrationEnv) createOrder(t *testing.T, input domain.CreateOrderInput) createOrderResponse {
	t.Helper()

	payload, err := json.Marshal(input)
	require.NoError(t, err)

	resp, err := http.Post(e.baseURL+"/api/v1/orders", "application/json", bytes.NewReader(payload))
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))

	var parsed createOrderResponse
	require.NoError(t, json.Unmarshal(body, &parsed))
	return parsed
}

func (e *integrationEnv) waitForOrderStatus(t *testing.T, orderID string, status domain.OrderStatus) domain.Order {
	t.Helper()

	var latest domain.Order
	require.Eventually(t, func() bool {
		order, err := e.getOrder(orderID)
		if err != nil {
			return false
		}
		latest = order
		return order.Status == status
	}, 15*time.Second, 200*time.Millisecond)

	return latest
}

func (e *integrationEnv) getOrder(orderID string) (domain.Order, error) {
	resp, err := http.Get(e.baseURL + "/api/v1/orders/" + orderID)
	if err != nil {
		return domain.Order{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return domain.Order{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var parsed getOrderResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return domain.Order{}, err
	}
	return parsed.Order, nil
}

func (e *integrationEnv) getMetrics(t *testing.T) string {
	t.Helper()

	require.Eventually(t, func() bool {
		body, err := httpGetBody(e.baseURL + "/metrics")
		if err != nil {
			return false
		}

		return strings.Contains(body, "app_order_create_duration_seconds") &&
			strings.Contains(body, "http_server_request_duration_seconds")
	}, 10*time.Second, 250*time.Millisecond)

	body, err := httpGetBody(e.baseURL + "/metrics")
	require.NoError(t, err)
	return body
}

func waitForReady(t *testing.T, baseURL string) {
	t.Helper()

	require.Eventually(t, func() bool {
		resp, err := http.Get(baseURL + "/readyz")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 15*time.Second, 200*time.Millisecond)
}

func httpGetBody(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	return string(body), nil
}

func startPostgresContainer(t *testing.T, ctx context.Context) (testcontainers.Container, string) {
	t.Helper()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:17-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_DB":       "go_golden_signals_demo",
				"POSTGRES_USER":     "postgres",
				"POSTGRES_PASSWORD": "postgres",
			},
			WaitingFor: wait.ForAll(
				wait.ForListeningPort(nat.Port("5432/tcp")),
				wait.ForLog("database system is ready to accept connections"),
			),
		},
	})
	require.NoError(t, err)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, nat.Port("5432/tcp"))
	require.NoError(t, err)

	databaseURL := fmt.Sprintf(
		"postgres://postgres:postgres@%s:%s/go_golden_signals_demo?sslmode=disable",
		host,
		port.Port(),
	)

	return container, databaseURL
}

func startRedisContainer(t *testing.T, ctx context.Context) (testcontainers.Container, string) {
	t.Helper()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:8.2-alpine",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForAll(
				wait.ForListeningPort(nat.Port("6379/tcp")),
				wait.ForLog("Ready to accept connections"),
			),
		},
	})
	require.NoError(t, err)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, nat.Port("6379/tcp"))
	require.NoError(t, err)

	return container, net.JoinHostPort(host, port.Port())
}
