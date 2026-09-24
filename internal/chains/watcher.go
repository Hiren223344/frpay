// Package chains defines the common interface every blockchain watcher
// implements. Adding a new chain to Frenix Pay means writing one
// ChainWatcher implementation in a new subpackage and registering it in
// main — nothing in internal/orders or internal/api needs to change.
package chains

import (
	"context"
	"math/big"
	"time"
)

// Chain identifies a supported network. Values match the `chain` column
// in the orders table.
type Chain string

const (
	Tron     Chain = "tron"
	Ethereum Chain = "ethereum"
	BSC      Chain = "bsc"
	Polygon  Chain = "polygon"
)

// DepositEvent is emitted whenever a watcher observes a USDT transfer to
// an address it is watching, and again on every subsequent poll while the
// transaction is still accumulating confirmations. Sinks (the orders
// service) must treat delivery as at-least-once and handle duplicates by
// (Chain, TxHash) idempotently.
type DepositEvent struct {
	Chain         Chain
	ToAddress     string
	TxHash        string
	Amount        *big.Int // raw token amount, smallest unit (e.g. 6dp for TRC20/most ERC20 USDT)
	TokenDecimals int32
	BlockHeight   int64
	Confirmations int64
	ObservedAt    time.Time
}

// DepositSink receives deposit events from every watcher. Implemented by
// internal/orders.Service. Watchers never import the orders package —
// this interface is the only coupling, and it points the other way, so
// chain implementations stay reusable independent of order logic.
type DepositSink interface {
	OnDeposit(ctx context.Context, event DepositEvent) error
}

// ChainWatcher is implemented once per network. A watcher only ever
// reads chain state; it has no access to any key capable of spending
// funds.
type ChainWatcher interface {
	// Chain identifies which network this watcher serves.
	Chain() Chain

	// RequiredConfirmations is the confirmation count this chain's
	// config requires before a deposit is considered final.
	RequiredConfirmations() int64

	// WatchAddress registers a deposit address to watch. Idempotent:
	// watching an address already being watched is a no-op.
	WatchAddress(ctx context.Context, address string) error

	// UnwatchAddress stops watching an address (called on order expiry
	// or confirmation, to bound the watch set to active orders).
	UnwatchAddress(ctx context.Context, address string) error

	// SeedConfirming primes in-flight confirmation tracking for an order
	// that already has a detected transaction (e.g. reloaded from the DB
	// after a restart), so confirmations keep accumulating without
	// re-scanning from genesis to rediscover it.
	SeedConfirming(ctx context.Context, address, txHash string, blockHeight int64) error

	// Run starts the watcher's polling loop and blocks until ctx is
	// cancelled or an unrecoverable error occurs. Deposits are reported
	// through the DepositSink supplied at construction.
	Run(ctx context.Context) error
}
