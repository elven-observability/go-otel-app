#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"

curl -sS \
  -X POST "${BASE_URL}/api/v1/orders" \
  -H 'Content-Type: application/json' \
  -d '{
    "customer_id": "cust-2001",
    "sku": "sku-latency-demo",
    "quantity": 1,
    "amount_cents": 8990,
    "simulation": {
      "payment_latency_ms": 700,
      "inventory_latency_ms": 500,
      "shipping_latency_ms": 900,
      "worker_latency_ms": 1200
    }
  }'
echo
