package webhook

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

const maxAttempts = 8

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, d *Delivery) error {
	_, err := r.db.NamedExecContext(ctx, `
		INSERT INTO webhook_deliveries (id, order_id, merchant_id, url, payload, signature, status, next_attempt_at, created_at)
		VALUES (:id, :order_id, :merchant_id, :url, :payload, :signature, :status, :next_attempt_at, :created_at)
	`, d)
	if err != nil {
		return fmt.Errorf("insert webhook delivery: %w", err)
	}
	return nil
}

// ListDue returns deliveries ready for a (re)try attempt, excluding
// those that have already exhausted their retry budget.
func (r *Repository) ListDue(ctx context.Context, limit int) ([]Delivery, error) {
	var out []Delivery
	err := r.db.SelectContext(ctx, &out, `
		SELECT * FROM webhook_deliveries
		WHERE status IN ('pending','failed')
		  AND attempt_count < $1
		  AND next_attempt_at <= now()
		ORDER BY next_attempt_at
		LIMIT $2
	`, maxAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("list due webhook deliveries: %w", err)
	}
	return out, nil
}

func (r *Repository) MarkDelivered(ctx context.Context, id string, responseStatus int, responseBody string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		SET status = 'delivered', attempt_count = attempt_count + 1, last_attempt_at = now(),
		    response_status = $2, response_body = $3
		WHERE id = $1
	`, id, responseStatus, truncate(responseBody, 2000))
	return err
}

func (r *Repository) MarkAttemptFailed(ctx context.Context, id string, responseStatus int, responseBody string, nextAttemptAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		SET status = 'failed', attempt_count = attempt_count + 1, last_attempt_at = now(),
		    response_status = $2, response_body = $3, next_attempt_at = $4
		WHERE id = $1
	`, id, responseStatus, truncate(responseBody, 2000), nextAttemptAt)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
