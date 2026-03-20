package httpadapter

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
)

type RouterDependencies struct {
	APIHandler       APIHandler
	SimulatorHandler SimulatorHandler
	MetricsHandler   http.Handler
	RequestTimeout   time.Duration
}

func NewRouter(deps RouterDependencies) http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(deps.RequestTimeout))

	router.Method(http.MethodGet, "/healthz", route("healthz", "/healthz", deps.APIHandler.Healthz))
	router.Method(http.MethodGet, "/readyz", route("readyz", "/readyz", deps.APIHandler.Readyz))
	router.Handle("/metrics", deps.MetricsHandler)

	router.Route("/api/v1", func(r chi.Router) {
		r.Method(http.MethodPost, "/orders", route("create_order", "/api/v1/orders", deps.APIHandler.CreateOrderHandler))
		r.Method(http.MethodGet, "/orders/{id}", route("get_order", "/api/v1/orders/{id}", deps.APIHandler.GetOrderHandler))
	})

	router.Route("/internal/sim", func(r chi.Router) {
		r.Method(http.MethodPost, "/payment/authorize", route("sim_payment_authorize", "/internal/sim/payment/authorize", deps.SimulatorHandler.PaymentAuthorize))
		r.Method(http.MethodPost, "/inventory/reserve", route("sim_inventory_reserve", "/internal/sim/inventory/reserve", deps.SimulatorHandler.InventoryReserve))
		r.Method(http.MethodPost, "/shipping/book", route("sim_shipping_book", "/internal/sim/shipping/book", deps.SimulatorHandler.ShippingBook))
	})

	return router
}

func route(operation, pattern string, handler http.HandlerFunc) http.Handler {
	return otelhttp.NewHandler(
		http.HandlerFunc(handler),
		operation,
		otelhttp.WithSpanNameFormatter(func(_ string, _ *http.Request) string {
			return pattern
		}),
		otelhttp.WithMetricAttributesFn(func(*http.Request) []attribute.KeyValue {
			return []attribute.KeyValue{attribute.String("http.route", pattern)}
		}),
	)
}
