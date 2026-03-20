package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/elven-observability/go-otel-app/internal/domain"
)

type Gateway struct {
	client  *http.Client
	baseURL string
}

type requestBody struct {
	OrderID    string `json:"order_id"`
	SKU        string `json:"sku"`
	Quantity   int    `json:"quantity"`
	LatencyMS  int64  `json:"latency_ms"`
	ShouldFail bool   `json:"should_fail"`
}

type responseBody struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func NewGateway(client *http.Client, baseURL string) *Gateway {
	return &Gateway{client: client, baseURL: baseURL}
}

func (g *Gateway) Authorize(ctx context.Context, order domain.Order, simulation domain.Simulation) error {
	return g.call(ctx, "/internal/sim/payment/authorize", requestBody{
		OrderID:    order.ID,
		SKU:        order.SKU,
		Quantity:   order.Quantity,
		LatencyMS:  simulation.PaymentLatencyMS,
		ShouldFail: simulation.FailPayment,
	}, "payment_dependency")
}

func (g *Gateway) Reserve(ctx context.Context, order domain.Order, simulation domain.Simulation) error {
	return g.call(ctx, "/internal/sim/inventory/reserve", requestBody{
		OrderID:    order.ID,
		SKU:        order.SKU,
		Quantity:   order.Quantity,
		LatencyMS:  simulation.InventoryLatencyMS,
		ShouldFail: simulation.FailInventory,
	}, "inventory_dependency")
}

func (g *Gateway) Book(ctx context.Context, order domain.Order, job domain.FulfillmentJob) error {
	return g.call(ctx, "/internal/sim/shipping/book", requestBody{
		OrderID:    order.ID,
		SKU:        order.SKU,
		Quantity:   order.Quantity,
		LatencyMS:  job.ShippingLatencyMS,
		ShouldFail: job.FailShipping,
	}, "shipping_dependency")
}

func (g *Gateway) call(ctx context.Context, path string, payload requestBody, errorType string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return domain.NewInternalError("failed to encode simulator payload")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return domain.NewInternalError("failed to build simulator request")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return domain.NewDependencyError("simulator_request_failed", err.Error(), errorType)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		var parsed responseBody
		_ = json.NewDecoder(resp.Body).Decode(&parsed)
		message := parsed.Error
		if message == "" {
			message = fmt.Sprintf("simulator returned status %d", resp.StatusCode)
		}
		return domain.NewDependencyError("dependency_failed", message, errorType)
	}

	return nil
}
