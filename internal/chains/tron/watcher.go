// Package tron implements chains.ChainWatcher for TRON, detecting
// USDT-TRC20 deposits by polling TronGrid's contract-events API for
// Transfer events emitted by the USDT contract — not by scanning every
// transaction in every block. This is the reference chain
// implementation; EVM chains follow the same shape in internal/chains/evm.
package tron

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/hiren223344/frpay/internal/chains"
	"github.com/hiren223344/frpay/internal/config"
	"github.com/hiren223344/frpay/internal/wallet"
)

// usdtDecimals is fixed for USDT-TRC20 (the only token this watcher
// tracks).
const usdtDecimals = 6

type pendingDeposit struct {
	address     string
	blockHeight int64
	amount      *big.Int
}

type Watcher struct {
	cfg     config.TronConfig
	client  *client
	sink    chains.DepositSink
	cursors chains.CursorStore
	logger  *slog.Logger

	mu      sync.RWMutex
	watched map[string]struct{}
	pending map[string]pendingDeposit // txHash -> deposit info
}

func NewWatcher(cfg config.TronConfig, sink chains.DepositSink, cursors chains.CursorStore, logger *slog.Logger) *Watcher {
	return &Watcher{
		cfg:     cfg,
		client:  newClient(cfg.APIURL, cfg.FallbackAPIURLs, cfg.APIKey),
		sink:    sink,
		cursors: cursors,
		logger:  logger.With("chain", "tron"),
		watched: make(map[string]struct{}),
		pending: make(map[string]pendingDeposit),
	}
}

func (w *Watcher) Chain() chains.Chain { return chains.Tron }

func (w *Watcher) RequiredConfirmations() int64 { return w.cfg.RequiredConfirms }

func (w *Watcher) WatchAddress(_ context.Context, address string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.watched[address] = struct{}{}
	return nil
}

func (w *Watcher) UnwatchAddress(_ context.Context, address string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.watched, address)
	return nil
}

func (w *Watcher) SeedConfirming(_ context.Context, address, txHash string, blockHeight int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.watched[address] = struct{}{}
	if _, exists := w.pending[txHash]; !exists {
		w.pending[txHash] = pendingDeposit{address: address, blockHeight: blockHeight}
	}
	return nil
}

func (w *Watcher) Run(ctx context.Context) error {
	if !w.cfg.Enabled {
		<-ctx.Done()
		return nil
	}
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.pollOnce(ctx); err != nil {
				w.logger.Warn("poll cycle failed", "error", err)
			}
		}
	}
}

func (w *Watcher) pollOnce(ctx context.Context) error {
	latest, err := w.getLatestBlock(ctx)
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}

	if err := w.scanNewEvents(ctx, latest); err != nil {
		w.logger.Warn("scan new events failed", "error", err)
	}

	w.refreshConfirmations(ctx, latest)
	return nil
}

func (w *Watcher) getLatestBlock(ctx context.Context) (int64, error) {
	var resp nowBlockResponse
	if err := w.client.get(ctx, "/wallet/getnowblock", nil, &resp); err != nil {
		return 0, err
	}
	return resp.BlockHeader.RawData.Number, nil
}

const maxPagesPerPoll = 20
const eventsPageLimit = 200

func (w *Watcher) scanNewEvents(ctx context.Context, latestBlock int64) error {
	_, lastScannedAt, err := w.cursors.GetCursor(ctx, chains.Tron)
	if err != nil {
		return fmt.Errorf("load cursor: %w", err)
	}

	minTimestamp := lastScannedAt.UnixMilli() + 1
	if lastScannedAt.IsZero() {
		// First run: don't scan the USDT contract's entire event history,
		// just start a short lookback so nothing sent right before startup
		// is missed.
		minTimestamp = time.Now().Add(-10 * time.Minute).UnixMilli()
	}

	maxBlockSeen := int64(0)
	maxTimestampSeen := int64(0)
	fingerprint := ""

	for page := 0; page < maxPagesPerPoll; page++ {
		query := url.Values{}
		query.Set("event_name", "Transfer")
		query.Set("only_confirmed", "true")
		query.Set("order_by", "block_timestamp,asc")
		query.Set("min_block_timestamp", strconv.FormatInt(minTimestamp, 10))
		query.Set("limit", strconv.Itoa(eventsPageLimit))
		if fingerprint != "" {
			query.Set("fingerprint", fingerprint)
		}

		var resp contractEventsResponse
		path := fmt.Sprintf("/v1/contracts/%s/events", w.cfg.USDTContract)
		if err := w.client.get(ctx, path, query, &resp); err != nil {
			return fmt.Errorf("fetch contract events: %w", err)
		}

		for _, ev := range resp.Data {
			w.handleEvent(ev)
			if ev.BlockNumber > maxBlockSeen {
				maxBlockSeen = ev.BlockNumber
			}
			if ev.BlockTimestamp > maxTimestampSeen {
				maxTimestampSeen = ev.BlockTimestamp
			}
		}

		if resp.Meta.Fingerprint == "" || len(resp.Data) < eventsPageLimit {
			break
		}
		fingerprint = resp.Meta.Fingerprint
	}

	if maxTimestampSeen > 0 {
		if err := w.cursors.SetCursor(ctx, chains.Tron, maxBlockSeen, time.UnixMilli(maxTimestampSeen)); err != nil {
			return fmt.Errorf("save cursor: %w", err)
		}
	}
	return nil
}

func (w *Watcher) handleEvent(ev contractEvent) {
	toHex := ev.Result["to"]
	if toHex == "" {
		return
	}
	addr, err := wallet.TronAddressFromHex(toHex)
	if err != nil {
		w.logger.Debug("skip event with unparseable address", "to", toHex, "error", err)
		return
	}

	w.mu.RLock()
	_, isWatched := w.watched[addr]
	w.mu.RUnlock()
	if !isWatched {
		return
	}

	amount, ok := new(big.Int).SetString(ev.Result["value"], 10)
	if !ok {
		w.logger.Warn("skip event with unparseable amount", "tx", ev.TransactionID, "value", ev.Result["value"])
		return
	}

	w.mu.Lock()
	w.pending[ev.TransactionID] = pendingDeposit{address: addr, blockHeight: ev.BlockNumber, amount: amount}
	w.mu.Unlock()
}

func (w *Watcher) refreshConfirmations(ctx context.Context, latestBlock int64) {
	type item struct {
		txHash      string
		address     string
		blockHeight int64
		amount      *big.Int
	}

	w.mu.RLock()
	items := make([]item, 0, len(w.pending))
	for tx, p := range w.pending {
		items = append(items, item{tx, p.address, p.blockHeight, p.amount})
	}
	w.mu.RUnlock()

	for _, it := range items {
		w.emit(ctx, it.txHash, it.address, it.amount, it.blockHeight, latestBlock)

		confirmations := latestBlock - it.blockHeight + 1
		if confirmations >= w.cfg.RequiredConfirms {
			w.mu.Lock()
			delete(w.pending, it.txHash)
			w.mu.Unlock()
		}
	}
}

func (w *Watcher) emit(ctx context.Context, txHash, address string, amount *big.Int, blockHeight, latestBlock int64) {
	confirmations := latestBlock - blockHeight + 1
	if confirmations < 0 {
		confirmations = 0
	}
	event := chains.DepositEvent{
		Chain:         chains.Tron,
		ToAddress:     address,
		TxHash:        txHash,
		Amount:        amount,
		TokenDecimals: usdtDecimals,
		BlockHeight:   blockHeight,
		Confirmations: confirmations,
		ObservedAt:    time.Now(),
	}
	if err := w.sink.OnDeposit(ctx, event); err != nil {
		w.logger.Error("sink rejected deposit event", "tx", txHash, "error", err)
	}
}
