package chains

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Manager runs one goroutine per configured ChainWatcher. If one
// watcher's Run returns an error, its remaining siblings keep running
// until ctx itself is cancelled — one chain's RPC having a bad day
// shouldn't take down deposit detection for the others.
type Manager struct {
	watchers map[Chain]ChainWatcher
	logger   *slog.Logger
}

func NewManager(logger *slog.Logger, watchers ...ChainWatcher) *Manager {
	m := &Manager{watchers: make(map[Chain]ChainWatcher), logger: logger}
	for _, w := range watchers {
		m.watchers[w.Chain()] = w
	}
	return m
}

func (m *Manager) RequiredConfirmations(chain Chain) (int64, bool) {
	w, ok := m.watchers[chain]
	if !ok {
		return 0, false
	}
	return w.RequiredConfirmations(), true
}

// SeedPending delegates to the watcher registered for the given chain.
// It's the only entry point internal/orders needs into this package
// beyond Chains()/RequiredConfirmations(), so adding a new chain never
// requires a change here or in the orders/API layers — just a new
// ChainWatcher registered with NewManager.
func (m *Manager) SeedPending(ctx context.Context, chain Chain, transfer PendingTransfer) error {
	w, ok := m.watchers[chain]
	if !ok {
		return fmt.Errorf("no watcher registered for chain %q", chain)
	}
	return w.SeedPending(ctx, transfer)
}

func (m *Manager) Chains() []Chain {
	chains := make([]Chain, 0, len(m.watchers))
	for c := range m.watchers {
		chains = append(chains, c)
	}
	return chains
}

// Run blocks until ctx is cancelled, running every watcher concurrently.
func (m *Manager) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for chain, watcher := range m.watchers {
		wg.Add(1)
		go func(chain Chain, watcher ChainWatcher) {
			defer wg.Done()
			m.logger.Info("chain watcher starting", "chain", chain)
			err := watcher.Run(ctx)
			if err != nil && ctx.Err() == nil {
				m.logger.Error("chain watcher exited unexpectedly", "chain", chain, "error", err)
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("watcher %s: %w", chain, err)
				}
				mu.Unlock()
				return
			}
			m.logger.Info("chain watcher stopped", "chain", chain)
		}(chain, watcher)
	}

	wg.Wait()
	return firstErr
}
