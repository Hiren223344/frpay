package orders

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ReviewReason categorizes why a transfer needs a human to look at it:
// real money was received that this service couldn't (or only
// partially could) attribute to an order automatically.
type ReviewReason string

const (
	ReasonUnmatched ReviewReason = "unmatched"
	ReasonUnderpaid ReviewReason = "underpaid"
)

// LogReviewItem records a transfer that needs manual attention. It never
// fails loudly on a duplicate (chain, tx_hash) — the same transfer can
// be reported by a watcher on every poll while unconfirmed, and only the
// first sighting needs a row.
func (r *Repository) LogReviewItem(ctx context.Context, chain, txHash string, amount decimal.Decimal, reason ReviewReason, relatedOrderID *uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO review_items (chain, tx_hash, amount, reason, related_order_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (chain, tx_hash) DO NOTHING
	`, chain, txHash, amount, reason, relatedOrderID)
	if err != nil {
		return fmt.Errorf("log review item: %w", err)
	}
	return nil
}
