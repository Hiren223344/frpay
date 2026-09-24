// Package audit records an append-only trail of every detected and
// confirmed on-chain transaction, independent of order state, so deposit
// activity can always be reconstructed even if an order's own row is
// later overwritten or an event was ultimately unmatched to any order.
package audit

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type Logger struct {
	db *sqlx.DB
}

func NewLogger(db *sqlx.DB) *Logger {
	return &Logger{db: db}
}

// Log writes one audit entry. orderID may be nil when an event could not
// be matched to any order (e.g. a deposit to a watched address whose
// order already expired).
func (l *Logger) Log(ctx context.Context, orderID *uuid.UUID, chain, txHash, eventType string, details map[string]interface{}) error {
	payload, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = l.db.ExecContext(ctx, `
		INSERT INTO audit_log (order_id, chain, tx_hash, event_type, details)
		VALUES ($1, $2, $3, $4, $5)
	`, orderID, chain, txHash, eventType, payload)
	return err
}
