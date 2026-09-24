package orders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var ErrNotFound = errors.New("order not found")

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// AllocateDerivationIndex atomically returns the next unused BIP44 index
// for the given wallet-family sequence key ("tron" or "evm"), so no two
// orders — ever, across restarts and concurrent requests — are handed
// the same deposit address.
func (r *Repository) AllocateDerivationIndex(ctx context.Context, sequenceKey string) (int64, error) {
	var idx int64
	err := r.db.GetContext(ctx, &idx, `
		INSERT INTO chain_address_sequences (sequence_key, next_index)
		VALUES ($1, 1)
		ON CONFLICT (sequence_key) DO UPDATE SET next_index = chain_address_sequences.next_index + 1
		RETURNING next_index - 1
	`, sequenceKey)
	if err != nil {
		return 0, fmt.Errorf("allocate derivation index: %w", err)
	}
	return idx, nil
}

func (r *Repository) CreateOrder(ctx context.Context, o *Order) error {
	_, err := r.db.NamedExecContext(ctx, `
		INSERT INTO orders (
			id, merchant_id, chain, token, derived_address, derivation_index,
			amount_usd, amount_token, rate_usd_per_token, status,
			required_confirmations, created_at, expires_at, updated_at
		) VALUES (
			:id, :merchant_id, :chain, :token, :derived_address, :derivation_index,
			:amount_usd, :amount_token, :rate_usd_per_token, :status,
			:required_confirmations, :created_at, :expires_at, :updated_at
		)
	`, o)
	if err != nil {
		return fmt.Errorf("insert order: %w", err)
	}
	return nil
}

func (r *Repository) GetOrder(ctx context.Context, id uuid.UUID) (*Order, error) {
	var o Order
	err := r.db.GetContext(ctx, &o, `SELECT * FROM orders WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get order: %w", err)
	}
	return &o, nil
}

// ListActive returns every order currently being watched for a chain
// (pending or confirming), used to reload each chain watcher's watch set
// on startup.
func (r *Repository) ListActive(ctx context.Context, chain string) ([]Order, error) {
	var out []Order
	err := r.db.SelectContext(ctx, &out, `
		SELECT * FROM orders WHERE chain = $1 AND status IN ('pending','confirming')
	`, chain)
	if err != nil {
		return nil, fmt.Errorf("list active orders: %w", err)
	}
	return out, nil
}

// ApplyDeposit atomically claims or updates an order's on-chain deposit
// state. It matches at most one order (by chain + derived_address,
// restricted to pending/confirming) and only writes a strictly
// non-decreasing confirmation count, so it is safe to call repeatedly —
// including with duplicate or out-of-order watcher events for the same
// tx_hash. When the update crosses required_confirmations for the first
// time, it transitions the order to "confirmed" atomically in the same
// statement.
//
// A nil, nil return means no active order matched (already
// confirmed/expired, or a stale duplicate) — not an error condition.
func (r *Repository) ApplyDeposit(ctx context.Context, chain, address, txHash string, blockHeight, confirmations int64) (*Order, error) {
	var o Order
	err := r.db.GetContext(ctx, &o, `
		UPDATE orders
		SET tx_hash = $3,
		    tx_block_height = $4,
		    confirmations = $5,
		    status = CASE WHEN $5 >= required_confirmations THEN 'confirmed' ELSE 'confirming' END,
		    confirmed_at = CASE WHEN $5 >= required_confirmations AND confirmed_at IS NULL THEN now() ELSE confirmed_at END,
		    updated_at = now()
		WHERE chain = $1
		  AND derived_address = $2
		  AND status IN ('pending','confirming')
		  AND (tx_hash IS NULL OR tx_hash = $3)
		  AND $5 >= confirmations
		RETURNING *
	`, chain, address, txHash, blockHeight, confirmations)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("apply deposit: %w", err)
	}
	return &o, nil
}

// ExpirePastDue transitions every pending order whose expiry has passed
// to "expired" and returns the rows that changed, so the caller can stop
// watching their addresses.
func (r *Repository) ExpirePastDue(ctx context.Context) ([]Order, error) {
	var out []Order
	err := r.db.SelectContext(ctx, &out, `
		UPDATE orders
		SET status = 'expired', updated_at = now()
		WHERE status = 'pending' AND expires_at < now()
		RETURNING *
	`)
	if err != nil {
		return nil, fmt.Errorf("expire past due orders: %w", err)
	}
	return out, nil
}

// IsHealthy performs a trivial connectivity check for readiness probes.
func (r *Repository) IsHealthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return r.db.PingContext(ctx)
}
