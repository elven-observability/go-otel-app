package httpadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type SimulatorHandler struct{}

type simulatorRequest struct {
	OrderID    string `json:"order_id"`
	SKU        string `json:"sku"`
	Quantity   int    `json:"quantity"`
	LatencyMS  int64  `json:"latency_ms"`
	ShouldFail bool   `json:"should_fail"`
}

func (h SimulatorHandler) PaymentAuthorize(w http.ResponseWriter, r *http.Request) {
	h.respond(w, r, "payment")
}

func (h SimulatorHandler) InventoryReserve(w http.ResponseWriter, r *http.Request) {
	h.respond(w, r, "inventory")
}

func (h SimulatorHandler) ShippingBook(w http.ResponseWriter, r *http.Request) {
	h.respond(w, r, "shipping")
}

func (h SimulatorHandler) respond(w http.ResponseWriter, r *http.Request, dependency string) {
	var request simulatorRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid simulator payload"})
		return
	}

	if request.LatencyMS > 0 {
		if err := sleepWithContext(r.Context(), time.Duration(request.LatencyMS)*time.Millisecond); err != nil {
			writeJSON(w, http.StatusRequestTimeout, map[string]string{"error": err.Error()})
			return
		}
	}

	if request.ShouldFail {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": dependency + " simulator forced failure"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"dependency": dependency,
	})
}

func sleepWithContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
