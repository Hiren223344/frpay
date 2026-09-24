package db

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/hiren223344/frpay/internal/chains"
)

// CursorStore implements chains.CursorStore against Postgres, the
// durable source of truth for how far each chain watcher has scanned.
type CursorStore struct {
	db *sqlx.DB
}

func NewCursorStore(db *sqlx.DB) *CursorStore {
	return &CursorStore{db: db}
}

func (s *CursorStore) GetCursor(ctx context.Context, chain chains.Chain) (int64, time.Time, error) {
	var row struct {
		LastScannedHeight int64      `db:"last_scanned_height"`
		LastScannedAt     *time.Time `db:"last_scanned_at"`
	}
	err := s.db.GetContext(ctx, &row, `
		INSERT INTO chain_cursors (chain, last_scanned_height)
		VALUES ($1, 0)
		ON CONFLICT (chain) DO UPDATE SET chain = EXCLUDED.chain
		RETURNING last_scanned_height, last_scanned_at
	`, string(chain))
	if err != nil {
		return 0, time.Time{}, err
	}
	if row.LastScannedAt == nil {
		return row.LastScannedHeight, time.Time{}, nil
	}
	return row.LastScannedHeight, *row.LastScannedAt, nil
}

func (s *CursorStore) SetCursor(ctx context.Context, chain chains.Chain, height int64, scannedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO chain_cursors (chain, last_scanned_height, last_scanned_at, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (chain) DO UPDATE
		SET last_scanned_height = EXCLUDED.last_scanned_height,
		    last_scanned_at = EXCLUDED.last_scanned_at,
		    updated_at = now()
	`, string(chain), height, scannedAt)
	return err
}
