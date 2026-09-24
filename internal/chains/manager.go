package chains

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Manager runs one goroutine per configured ChainWatcher and restarts a
// watcher's loop with backoff if it returns an error, rather than taking
// down the whole service when one chain's RPC is having a bad day.
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

func (m *Manager) WatcherFor(chain Chain) (ChainWatcher, bool) {
	w, ok := m.watchers[chain]
	return w, ok
}

// Watch, Unwatch and SeedConfirming delegate to the watcher registered
// for the given chain. They're the only entry points internal/orders
// needs into this package, so adding a new chain never requires a
// change here or in the orders/API layers — just a new ChainWatcher
// registered with NewManager.

func (m *Manager) Watch(ctx context.Context, chain Chain, address string) error {
	w, ok := m.watchers[chain]
	if !ok {
		return fmt.Errorf("no watcher registered for chain %q", chain)
	}
	return w.WatchAddress(ctx, address)
}

func (m *Manager) Unwatch(ctx context.Context, chain Chain, address string) error {
	w, ok := m.watchers[chain]
	if !ok {
		return fmt.Errorf("no watcher registered for chain %q", chain)
	}
	return w.UnwatchAddress(ctx, address)
}

func (m *Manager) SeedConfirming(ctx context.Context, chain Chain, address, txHash string, blockHeight int64) error {
	w, ok := m.watchers[chain]
	if !ok {
		return fmt.Errorf("no watcher registered for chain %q", chain)
	}
	return w.SeedConfirming(ctx, address, txHash, blockHeight)
}

func (m *Manager) RequiredConfirmations(chain Chain) (int64, bool) {
	w, ok := m.watchers[chain]
	if !ok {
		return 0, false
	}
	return w.RequiredConfirmations(), true
}

func (m *Manager) Chains() []Chain {
	chains := make([]Chain, 0, len(m.watchers))
	for c := range m.watchers {
		chains = append(chains, c)
	}
	return chains
}

// Run blocks until ctx is cancelled, running every watcher concurrently.
// If one watcher's Run returns an error, its remaining siblings keep
// running until ctx itself is cancelled — one chain having a bad day
// shouldn't take down deposit detection for the others.
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
