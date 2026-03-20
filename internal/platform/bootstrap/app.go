package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/XSAM/otelsql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/extra/redisotel/v9"
	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/elven-observability/go-otel-app/internal/adapters/http"
	"github.com/elven-observability/go-otel-app/internal/adapters/postgres"
	redisadapter "github.com/elven-observability/go-otel-app/internal/adapters/redis"
	"github.com/elven-observability/go-otel-app/internal/adapters/simulator"
	"github.com/elven-observability/go-otel-app/internal/adapters/worker"
	"github.com/elven-observability/go-otel-app/internal/application"
	"github.com/elven-observability/go-otel-app/internal/platform/config"
	"github.com/elven-observability/go-otel-app/internal/platform/database"
	"github.com/elven-observability/go-otel-app/internal/platform/observability"
)

type App struct {
	Config         config.Config
	Server         *http.Server
	Worker         *worker.Worker
	Telemetry      *observability.Telemetry
	DB             *sql.DB
	Redis          *goredis.Client
	DBRegistration metric.Registration
	Logger         *slog.Logger
}

func New(ctx context.Context, cfg config.Config) (*App, error) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{}))
	var cleanup []func(context.Context) error

	telemetry, err := observability.New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cleanup = append(cleanup, func(ctx context.Context) error {
		return telemetry.Shutdown(ctx)
	})

	db, reg, err := openDatabase(ctx, cfg)
	if err != nil {
		return nil, shutdownOnError(ctx, cleanup, err)
	}
	cleanup = append(cleanup, func(context.Context) error {
		if reg != nil {
			return reg.Unregister()
		}
		return nil
	})
	cleanup = append(cleanup, func(context.Context) error {
		return db.Close()
	})

	if err := database.Migrate(ctx, db); err != nil {
		return nil, shutdownOnError(ctx, cleanup, err)
	}

	redisClient, err := openRedis(ctx, cfg)
	if err != nil {
		return nil, shutdownOnError(ctx, cleanup, err)
	}
	cleanup = append(cleanup, func(context.Context) error {
		return redisClient.Close()
	})

	orderRepo := postgres.NewOrderRepository(db)
	orderCache := redisadapter.NewOrderCache(redisClient, cfg.CacheTTL)
	queue := redisadapter.NewFulfillmentQueue(redisClient, cfg.QueueName, cfg.DLQName)

	httpClient := &http.Client{
		Timeout:   cfg.RequestTimeout,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
	simulatorGateway := simulator.NewGateway(httpClient, cfg.PublicBaseURL)

	createOrder := application.CreateOrderUseCase{
		Repo:      orderRepo,
		Cache:     orderCache,
		Queue:     queue,
		Payment:   simulatorGateway,
		Inventory: simulatorGateway,
		Metrics:   telemetry.Metrics,
		Tracer:    telemetry.Tracer,
	}
	getOrder := application.GetOrderUseCase{
		Repo:   orderRepo,
		Cache:  orderCache,
		Tracer: telemetry.Tracer,
	}
	processFulfillment := application.ProcessFulfillmentUseCase{
		Repo:        orderRepo,
		Cache:       orderCache,
		Queue:       queue,
		Shipping:    simulatorGateway,
		Metrics:     telemetry.Metrics,
		Tracer:      telemetry.Tracer,
		MaxAttempts: cfg.WorkerMaxAttempts,
	}

	apiHandler := httpadapter.APIHandler{
		CreateOrder: createOrder,
		GetOrder:    getOrder,
		ReadyCheck: func(ctx context.Context) error {
			if err := orderRepo.Ping(ctx); err != nil {
				return err
			}
			return queue.Ping(ctx)
		},
	}

	handler := httpadapter.NewRouter(httpadapter.RouterDependencies{
		APIHandler:       apiHandler,
		SimulatorHandler: httpadapter.SimulatorHandler{},
		MetricsHandler:   telemetry.PrometheusHandler,
		RequestTimeout:   cfg.RequestTimeout,
	})

	return &App{
		Config:         cfg,
		Server:         &http.Server{Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second},
		Worker:         worker.New(queue, processFulfillment, telemetry.Metrics, logger, cfg.WorkerPollTimeout),
		Telemetry:      telemetry,
		DB:             db,
		Redis:          redisClient,
		DBRegistration: reg,
		Logger:         logger,
	}, nil
}

func (a *App) Shutdown(ctx context.Context) error {
	var err error
	if a.DBRegistration != nil {
		err = errors.Join(err, a.DBRegistration.Unregister())
	}
	if a.Redis != nil {
		err = errors.Join(err, a.Redis.Close())
	}
	if a.DB != nil {
		err = errors.Join(err, a.DB.Close())
	}
	if a.Telemetry != nil {
		err = errors.Join(err, a.Telemetry.Shutdown(ctx))
	}
	return err
}

func openDatabase(ctx context.Context, cfg config.Config) (*sql.DB, metric.Registration, error) {
	db, err := otelsql.Open("pgx", cfg.DatabaseURL,
		otelsql.WithAttributes(
			attribute.String("db.system.name", "postgresql"),
		),
		otelsql.WithTracerProvider(otel.GetTracerProvider()),
		otelsql.WithMeterProvider(otel.GetMeterProvider()),
	)
	if err != nil {
		return nil, nil, err
	}

	reg, err := otelsql.RegisterDBStatsMetrics(db,
		otelsql.WithMeterProvider(otel.GetMeterProvider()),
	)
	if err != nil {
		return nil, nil, err
	}

	if err := db.PingContext(ctx); err != nil {
		return nil, nil, err
	}

	return db, reg, nil
}

func openRedis(ctx context.Context, cfg config.Config) (*goredis.Client, error) {
	client := goredis.NewClient(&goredis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	if err := redisotel.InstrumentTracing(client,
		redisotel.WithTracerProvider(otel.GetTracerProvider()),
		redisotel.WithDBStatement(true),
	); err != nil {
		return nil, err
	}

	if err := redisotel.InstrumentMetrics(client,
		redisotel.WithMeterProvider(otel.GetMeterProvider()),
	); err != nil {
		return nil, err
	}

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	return client, nil
}

func shutdownOnError(ctx context.Context, cleanup []func(context.Context) error, cause error) error {
	var joined error
	for i := len(cleanup) - 1; i >= 0; i-- {
		joined = errors.Join(joined, cleanup[i](ctx))
	}
	return errors.Join(cause, joined)
}
