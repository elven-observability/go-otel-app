package httpadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/trace"

	"github.com/elven-observability/go-otel-app/internal/application"
	"github.com/elven-observability/go-otel-app/internal/domain"
)

type APIHandler struct {
	CreateOrder application.CreateOrderUseCase
	GetOrder    application.GetOrderUseCase
	ReadyCheck  func(context.Context) error
}

func (h APIHandler) Healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC(),
	})
}

func (h APIHandler) Readyz(w http.ResponseWriter, r *http.Request) {
	if err := h.ReadyCheck(r.Context()); err != nil {
		writeError(w, r, domain.NewInternalError("dependencies are not ready"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ready",
		"time":   time.Now().UTC(),
	})
}

func (h APIHandler) CreateOrderHandler(w http.ResponseWriter, r *http.Request) {
	var input domain.CreateOrderInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, domain.NewValidationError("invalid JSON payload"))
		return
	}

	order, err := h.CreateOrder.Execute(r.Context(), input)
	if err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"trace_id": currentTraceID(r.Context()),
		"order":    order,
	})
}

func (h APIHandler) GetOrderHandler(w http.ResponseWriter, r *http.Request) {
	order, err := h.GetOrder.Execute(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"trace_id": currentTraceID(r.Context()),
		"order":    order,
	})
}

func currentTraceID(ctx context.Context) string {
	spanContext := trace.SpanFromContext(ctx).SpanContext()
	if !spanContext.IsValid() {
		return ""
	}

	return spanContext.TraceID().String()
}
