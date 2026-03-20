#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"

curl -sS \
  -X POST "${BASE_URL}/api/v1/orders" \
  -H 'Content-Type: application/json' \
  -d '{
    "customer_id": "cust-4001",
    "sku": "sku-shipping-dlq",
    "quantity": 1,
    "amount_cents": 15990,
    "simulation": {
      "fail_shipping": true
    }
  }'
echo
