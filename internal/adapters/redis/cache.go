package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/elven-observability/go-otel-app/internal/domain"
)

type OrderCache struct {
	client *goredis.Client
	ttl    time.Duration
}

func NewOrderCache(client *goredis.Client, ttl time.Duration) *OrderCache {
	return &OrderCache{client: client, ttl: ttl}
}

func (c *OrderCache) Set(ctx context.Context, order domain.Order) error {
	payload, err := json.Marshal(order)
	if err != nil {
		return err
	}

	return c.client.Set(ctx, cacheKey(order.ID), payload, c.ttl).Err()
}

func (c *OrderCache) Get(ctx context.Context, id string) (domain.Order, error) {
	payload, err := c.client.Get(ctx, cacheKey(id)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return domain.Order{}, domain.NewNotFoundError("order cache miss")
		}
		return domain.Order{}, err
	}

	var order domain.Order
	if err := json.Unmarshal(payload, &order); err != nil {
		return domain.Order{}, fmt.Errorf("decode cached order: %w", err)
	}

	return order, nil
}

func (c *OrderCache) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func cacheKey(id string) string {
	return "orders:" + id
}
