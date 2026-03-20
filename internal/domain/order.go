package domain

import "time"

type OrderStatus string

const (
	OrderStatusPending   OrderStatus = "pending"
	OrderStatusQueued    OrderStatus = "queued"
	OrderStatusFulfilled OrderStatus = "fulfilled"
	OrderStatusFailed    OrderStatus = "failed"
)

type Simulation struct {
	PaymentLatencyMS   int64 `json:"payment_latency_ms"`
	InventoryLatencyMS int64 `json:"inventory_latency_ms"`
	ShippingLatencyMS  int64 `json:"shipping_latency_ms"`
	FailPayment        bool  `json:"fail_payment"`
	FailInventory      bool  `json:"fail_inventory"`
	FailShipping       bool  `json:"fail_shipping"`
	WorkerLatencyMS    int64 `json:"worker_latency_ms"`
}

type CreateOrderInput struct {
	CustomerID  string     `json:"customer_id"`
	SKU         string     `json:"sku"`
	Quantity    int        `json:"quantity"`
	AmountCents int64      `json:"amount_cents"`
	Simulation  Simulation `json:"simulation"`
}

type Order struct {
	ID            string      `json:"id"`
	CustomerID    string      `json:"customer_id"`
	SKU           string      `json:"sku"`
	Quantity      int         `json:"quantity"`
	AmountCents   int64       `json:"amount_cents"`
	Status        OrderStatus `json:"status"`
	FailureReason *string     `json:"failure_reason,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

type FulfillmentJob struct {
	OrderID           string `json:"order_id"`
	SKU               string `json:"sku"`
	Quantity          int    `json:"quantity"`
	ShippingLatencyMS int64  `json:"shipping_latency_ms"`
	FailShipping      bool   `json:"fail_shipping"`
	WorkerLatencyMS   int64  `json:"worker_latency_ms"`
	Attempt           int    `json:"attempt"`
}
