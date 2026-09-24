// Package chains defines the common interface every blockchain watcher
// implements. Each chain has exactly ONE fixed receiving address,
// configured once — not one per order — so a watcher's only job is to
// continuously scan that address and report every incoming USDT
// transfer it sees. Matching a transfer to a pending order by amount is
// the orders package's job, not the watcher's, which is what keeps
// adding a new chain down to "implement this interface" with no changes
// to internal/orders or internal/api.
package chains

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// PendingTransfer seeds a watcher's in-flight confirmation tracking for
// a transfer it already reported before a restart (an order still in
// "confirming"), so confirmations keep accumulating without needing to
// rediscover the transaction from a cursor that has already moved past
// it.
type PendingTransfer struct {
	TxHash      string
	Amount      decimal.Decimal
	BlockHeight int64
}

// Chain identifies a supported network. Values match the `chain` column
// in the orders table.
type Chain string

const (
	Tron    Chain = "tron"
	TON     Chain = "ton"
	Polygon Chain = "polygon"
	BSC     Chain = "bsc"
)

// TransferEvent is emitted whenever a watcher observes a USDT transfer
// to its chain's fixed address, and again on every subsequent poll while
// it's still accumulating confirmations. Sinks (the orders service) must
// treat delivery as at-least-once and handle duplicates by (Chain,
// TxHash) idempotently.
type TransferEvent struct {
	Chain Chain
	// TxHash uniquely identifies the transfer on its chain.
	TxHash string
	// Amount is the human-readable USDT amount (already adjusted for the
	// token's on-chain decimals), since USDT is treated as pegged 1:1 to
	// USD throughout this service.
	Amount        decimal.Decimal
	BlockHeight   int64
	Confirmations int64
	ObservedAt    time.Time
}

// TransferSink receives transfer events from every watcher. Implemented
// by internal/orders.Service. Watchers never import the orders package —
// this interface is the only coupling, and it points the other way, so
// chain implementations stay reusable independent of order-matching
// logic.
type TransferSink interface {
	OnTransfer(ctx context.Context, event TransferEvent) error
}

// ChainWatcher is implemented once per network. A watcher only ever
// reads chain state against its one configured address; it has no
// access to any key capable of spending funds.
type ChainWatcher interface {
	// Chain identifies which network this watcher serves.
	Chain() Chain

	// RequiredConfirmations is the confirmation count this chain's
	// config requires before a transfer is considered final.
	RequiredConfirmations() int64

	// SeedPending primes in-flight confirmation tracking for a transfer
	// already known (e.g. reloaded from the DB after a restart), so
	// confirmations keep accumulating for it even though the watcher's
	// scan cursor has already moved past the block it was detected in.
	SeedPending(ctx context.Context, transfer PendingTransfer) error

	// Run starts the watcher's polling loop and blocks until ctx is
	// cancelled or an unrecoverable error occurs. Transfers are reported
	// through the TransferSink supplied at construction.
	Run(ctx context.Context) error
}
