package redis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/elven-observability/go-otel-app/internal/domain"
	"github.com/elven-observability/go-otel-app/internal/ports"
)

type FulfillmentQueue struct {
	client    *goredis.Client
	queueName string
	dlqName   string
}

func NewFulfillmentQueue(client *goredis.Client, queueName, dlqName string) *FulfillmentQueue {
	return &FulfillmentQueue{client: client, queueName: queueName, dlqName: dlqName}
}

func (q *FulfillmentQueue) Enqueue(ctx context.Context, job domain.FulfillmentJob) error {
	payload, err := json.Marshal(job)
	if err != nil {
		return err
	}

	return q.client.LPush(ctx, q.queueName, payload).Err()
}

func (q *FulfillmentQueue) Dequeue(ctx context.Context, wait time.Duration) (domain.FulfillmentJob, error) {
	// Redis BRPOP rounds sub-second waits up to one second and logs a warning.
	// Clamp here so the worker behaves consistently across local and CI runs.
	if wait > 0 && wait < time.Second {
		wait = time.Second
	}

	result, err := q.client.BRPop(ctx, wait, q.queueName).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return domain.FulfillmentJob{}, ports.ErrQueueEmpty
		}
		return domain.FulfillmentJob{}, err
	}

	if len(result) != 2 {
		return domain.FulfillmentJob{}, ports.ErrQueueEmpty
	}

	var job domain.FulfillmentJob
	if err := json.Unmarshal([]byte(result[1]), &job); err != nil {
		return domain.FulfillmentJob{}, err
	}

	return job, nil
}

func (q *FulfillmentQueue) SendToDLQ(ctx context.Context, job domain.FulfillmentJob, reason string) error {
	payload, err := json.Marshal(struct {
		Reason string                `json:"reason"`
		Job    domain.FulfillmentJob `json:"job"`
	}{
		Reason: reason,
		Job:    job,
	})
	if err != nil {
		return err
	}

	return q.client.LPush(ctx, q.dlqName, payload).Err()
}

func (q *FulfillmentQueue) Depth(ctx context.Context) (int64, error) {
	return q.client.LLen(ctx, q.queueName).Result()
}

func (q *FulfillmentQueue) Ping(ctx context.Context) error {
	return q.client.Ping(ctx).Err()
}
