#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"

curl -sS \
  -X POST "${BASE_URL}/api/v1/orders" \
  -H 'Content-Type: application/json' \
  -d '{
    "customer_id": "cust-3001",
    "sku": "sku-inventory-failure",
    "quantity": 1,
    "amount_cents": 4990,
    "simulation": {
      "fail_inventory": true
    }
  }'
echo
