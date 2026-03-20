SHELL := /bin/bash

.PHONY: run test integration-test up down smoke tidy

run:
	@set -a; [ -f .env ] && source ./.env; set +a; go run ./cmd/api

test:
	@go test ./...

integration-test:
	@set -a; [ -f .env ] && source ./.env; set +a; go test -tags=integration ./test/integration/...

up:
	@docker compose up -d

down:
	@docker compose down -v

smoke:
	@set -a; [ -f .env ] && source ./.env; set +a; \
	bash ./examples/curl/01-health.sh && \
	bash ./examples/curl/02-create-order.sh && \
	bash ./examples/curl/03-create-order-with-latency.sh

tidy:
	@go mod tidy
