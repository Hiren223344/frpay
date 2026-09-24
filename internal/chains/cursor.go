package chains

import (
	"context"
	"time"
)

// CursorStore persists each watcher's last-scanned block/ledger position
// so a restart resumes exactly where it left off instead of re-scanning
// from genesis or, worse, silently skipping the gap since shutdown.
type CursorStore interface {
	GetCursor(ctx context.Context, chain Chain) (height int64, lastScannedAt time.Time, err error)
	SetCursor(ctx context.Context, chain Chain, height int64, scannedAt time.Time) error
}
