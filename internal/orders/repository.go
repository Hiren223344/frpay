package orders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

var ErrNotFound = errors.New("order not found")

// uniqueViolation is Postgres error code 23505.
const uniqueViolation = "23505"

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// CreateWithUniqueAmount inserts a new order whose expected_amount is
// baseAmountUSD plus a random offset (offsetMin..offsetMax
// ten-thousandths of a dollar). A unique index on (chain,
// expected_amount) for pending orders means a colliding offset fails
// the insert with a unique-violation, which this retries with a fresh
// offset up to maxRetries times — collisions are expected and handled,
// not treated as an error, until the retry budget is actually exhausted.
func (r *Repository) CreateWithUniqueAmount(
	ctx context.Context,
	userID, chain string,
	baseAmountUSD decimal.Decimal,
	requiredConfirmations int64,
	expiry time.Duration,
	offsetMin, offsetMax, maxRetries int,
) (*Order, error) {
	if offsetMax < offsetMin {
		return nil, fmt.Errorf("invalid offset range [%d,%d]", offsetMin, offsetMax)
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		offsetTenThousandths := offsetMin
		if offsetMax > offsetMin {
			offsetTenThousandths = offsetMin + rand.Intn(offsetMax-offsetMin+1)
		}
		offset := decimal.New(int64(offsetTenThousandths), -4)
		expected := baseAmountUSD.Add(offset)

		now := time.Now().UTC()
		order := &Order{
			ID:                    uuid.New(),
			UserID:                userID,
			Chain:                 chain,
			Token:                 "USDT",
			BaseAmountUSD:         baseAmountUSD,
			ExpectedAmount:        expected,
			Status:                StatusPending,
			RequiredConfirmations: requiredConfirmations,
			CreatedAt:             now,
			ExpiresAt:             now.Add(expiry),
			UpdatedAt:             now,
		}

		_, err := r.db.NamedExecContext(ctx, `
			INSERT INTO orders (
				id, user_id, chain, token, base_amount_usd, expected_amount,
				status, required_confirmations, created_at, expires_at, updated_at
			) VALUES (
				:id, :user_id, :chain, :token, :base_amount_usd, :expected_amount,
				:status, :required_confirmations, :created_at, :expires_at, :updated_at
			)
		`, order)
		if err == nil {
			return order, nil
		}
		if !isUniqueViolation(err) {
			return nil, fmt.Errorf("insert order: %w", err)
		}
		// Collision on (chain, expected_amount): another pending order
		// already has this exact amount. Retry with a new offset.
	}
	return nil, fmt.Errorf("could not allocate a unique expected_amount for chain %q after %d attempts", chain, maxRetries)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
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

// ListConfirming returns every order currently accumulating
// confirmations on a chain, used to reseed each chain watcher's
// in-flight transfer tracking on startup.
func (r *Repository) ListConfirming(ctx context.Context, chain string) ([]Order, error) {
	var out []Order
	err := r.db.SelectContext(ctx, &out, `
		SELECT * FROM orders WHERE chain = $1 AND status = 'confirming'
	`, chain)
	if err != nil {
		return nil, fmt.Errorf("list confirming orders: %w", err)
	}
	return out, nil
}

// TxAlreadyClaimed reports whether any order (in any status) has already
// been attributed this tx_hash on this chain.
func (r *Repository) TxAlreadyClaimed(ctx context.Context, chain, txHash string) (bool, error) {
	var exists bool
	err := r.db.GetContext(ctx, &exists, `
		SELECT EXISTS(SELECT 1 FROM orders WHERE chain = $1 AND tx_hash = $2)
	`, chain, txHash)
	if err != nil {
		return false, fmt.Errorf("check tx claimed: %w", err)
	}
	return exists, nil
}

// UpdateConfirmations advances a transfer's confirmation count on the
// order that already claimed it. It only ever writes a strictly
// non-decreasing count and only while the order is still "confirming",
// so it's safe to call repeatedly with duplicate or out-of-order watcher
// events. Crossing required_confirmations transitions the order to
// "confirmed" in the same statement. A nil, nil return means no
// "confirming" order has this tx_hash (already confirmed, or a stale
// duplicate) — not an error.
func (r *Repository) UpdateConfirmations(ctx context.Context, chain, txHash string, blockHeight, confirmations int64) (*Order, error) {
	var o Order
	err := r.db.GetContext(ctx, &o, `
		UPDATE orders
		SET tx_block_height = $3,
		    confirmations = $4,
		    status = CASE WHEN $4 >= required_confirmations THEN 'confirmed' ELSE 'confirming' END,
		    confirmed_at = CASE WHEN $4 >= required_confirmations AND confirmed_at IS NULL THEN now() ELSE confirmed_at END,
		    updated_at = now()
		WHERE chain = $1 AND tx_hash = $2 AND status = 'confirming' AND $4 >= confirmations
		RETURNING *
	`, chain, txHash, blockHeight, confirmations)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update confirmations: %w", err)
	}
	return &o, nil
}

// ClaimExactMatch atomically attributes a brand-new transfer to the
// oldest still-unclaimed pending order on this chain whose
// expected_amount is within tolerance of amount. FOR UPDATE SKIP LOCKED
// makes this safe if multiple watcher goroutines ever race on the same
// chain. A nil, nil return means no pending order's expected_amount is
// within tolerance — not an error.
func (r *Repository) ClaimExactMatch(ctx context.Context, chain, txHash string, amount decimal.Decimal, blockHeight, confirmations int64, tolerance decimal.Decimal) (*Order, error) {
	low := amount.Sub(tolerance)
	high := amount.Add(tolerance)

	var o Order
	err := r.db.GetContext(ctx, &o, `
		UPDATE orders
		SET tx_hash = $2,
		    tx_block_height = $3,
		    received_amount = $4,
		    confirmations = $5,
		    status = CASE WHEN $5 >= required_confirmations THEN 'confirmed' ELSE 'confirming' END,
		    confirmed_at = CASE WHEN $5 >= required_confirmations THEN now() ELSE NULL END,
		    updated_at = now()
		WHERE id = (
			SELECT id FROM orders
			WHERE chain = $1 AND status = 'pending' AND tx_hash IS NULL
			  AND expected_amount BETWEEN $6 AND $7
			ORDER BY expires_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING *
	`, chain, txHash, blockHeight, amount, confirmations, low, high)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim exact match: %w", err)
	}
	return &o, nil
}

// ClaimUnderpaid looks for the closest pending order (by expected_amount)
// that amount falls short of by no more than maxGapPercent, and marks it
// underpaid. Run inside a transaction with FOR UPDATE SKIP LOCKED so
// concurrent claims can't pick the same order. A nil, nil return means
// no pending order's shortfall is within the configured gap — not an
// error; the caller should log the transfer as unmatched instead.
func (r *Repository) ClaimUnderpaid(ctx context.Context, chain, txHash string, amount decimal.Decimal, blockHeight int64, maxGapPercent decimal.Decimal) (*Order, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var candidates []Order
	err = tx.SelectContext(ctx, &candidates, `
		SELECT * FROM orders
		WHERE chain = $1 AND status = 'pending' AND tx_hash IS NULL AND expected_amount > $2
		ORDER BY expected_amount ASC
		FOR UPDATE SKIP LOCKED
	`, chain, amount)
	if err != nil {
		return nil, fmt.Errorf("select underpayment candidates: %w", err)
	}

	var best *Order
	var bestGapPercent decimal.Decimal
	for i := range candidates {
		c := candidates[i]
		gap := c.ExpectedAmount.Sub(amount)
		gapPercent := gap.Div(c.ExpectedAmount).Mul(decimal.NewFromInt(100))
		if gapPercent.GreaterThan(maxGapPercent) {
			continue
		}
		if best == nil || gapPercent.LessThan(bestGapPercent) {
			best = &c
			bestGapPercent = gapPercent
		}
	}
	if best == nil {
		return nil, nil
	}

	var updated Order
	err = tx.GetContext(ctx, &updated, `
		UPDATE orders
		SET tx_hash = $2, tx_block_height = $3, received_amount = $4,
		    status = 'underpaid', updated_at = now()
		WHERE id = $1
		RETURNING *
	`, best.ID, txHash, blockHeight, amount)
	if err != nil {
		return nil, fmt.Errorf("mark underpaid: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit underpayment claim: %w", err)
	}
	return &updated, nil
}

// ExpirePastDue transitions every pending order whose expiry has passed
// to "expired" and returns the rows that changed. Only "pending" orders
// expire — one already "confirming" has a real transfer in flight and
// must run to completion regardless of the original window.
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
