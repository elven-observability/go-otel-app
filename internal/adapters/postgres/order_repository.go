package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/elven-observability/go-otel-app/internal/domain"
)

type OrderRepository struct {
	db *sql.DB
}

func NewOrderRepository(db *sql.DB) *OrderRepository {
	return &OrderRepository{db: db}
}

func (r *OrderRepository) Create(ctx context.Context, order domain.Order) (domain.Order, error) {
	const query = `
		INSERT INTO orders (
			id, customer_id, sku, quantity, amount_cents, status, failure_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at, updated_at
	`

	err := r.db.QueryRowContext(ctx, query,
		order.ID,
		order.CustomerID,
		order.SKU,
		order.Quantity,
		order.AmountCents,
		order.Status,
		order.FailureReason,
	).Scan(&order.CreatedAt, &order.UpdatedAt)
	if err != nil {
		return domain.Order{}, err
	}

	return order, nil
}

func (r *OrderRepository) GetByID(ctx context.Context, id string) (domain.Order, error) {
	const query = `
		SELECT id, customer_id, sku, quantity, amount_cents, status, failure_reason, created_at, updated_at
		FROM orders
		WHERE id = $1
	`

	var order domain.Order
	if err := r.db.QueryRowContext(ctx, query, id).Scan(
		&order.ID,
		&order.CustomerID,
		&order.SKU,
		&order.Quantity,
		&order.AmountCents,
		&order.Status,
		&order.FailureReason,
		&order.CreatedAt,
		&order.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Order{}, domain.NewNotFoundError("order not found")
		}
		return domain.Order{}, err
	}

	return order, nil
}

func (r *OrderRepository) UpdateStatus(ctx context.Context, id string, status domain.OrderStatus, failureReason *string) error {
	const query = `
		UPDATE orders
		SET status = $2,
		    failure_reason = $3,
		    updated_at = NOW()
		WHERE id = $1
	`

	result, err := r.db.ExecContext(ctx, query, id, status, failureReason)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return domain.NewNotFoundError("order not found")
	}

	return nil
}

func (r *OrderRepository) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}
