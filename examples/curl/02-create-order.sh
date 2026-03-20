#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"

curl -sS \
  -X POST "${BASE_URL}/api/v1/orders" \
  -H 'Content-Type: application/json' \
  -d '{
    "customer_id": "cust-1001",
    "sku": "sku-golden-signal",
    "quantity": 2,
    "amount_cents": 12990
  }'
echo
