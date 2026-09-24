// Package evm implements chains.ChainWatcher for EVM-compatible chains
// (Polygon, BSC, and any future EVM chain) with a single implementation
// parameterized by config.EVMConfig. It detects USDT transfers to one
// fixed address via eth_getLogs, filtered server-side to the USDT
// contract's Transfer topic and that address — never a full block scan.
package evm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/hiren223344/frpay/internal/chains"
	"github.com/hiren223344/frpay/internal/config"
)

// transferTopic is keccak256("Transfer(address,address,uint256)"), the
// standard ERC20 Transfer event signature shared by every EVM chain.
const transferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

const usdtDecimals = 6 // true for USDT on Polygon and BSC

type pendingTransfer struct {
	amount      decimal.Decimal
	blockHeight int64
}

type Watcher struct {
	cfg          config.EVMConfig
	chain        chains.Chain
	addressTopic string // cfg.Address padded to a 32-byte topic, precomputed once
	client       *client
	sink         chains.TransferSink
	cursors      chains.CursorStore
	logger       *slog.Logger

	mu      sync.Mutex
	pending map[string]pendingTransfer
}

func NewWatcher(chain chains.Chain, cfg config.EVMConfig, sink chains.TransferSink, cursors chains.CursorStore, logger *slog.Logger) *Watcher {
	return &Watcher{
		cfg:          cfg,
		chain:        chain,
		addressTopic: addressToTopic(cfg.Address),
		client:       newClient(cfg.RPCURL, cfg.FallbackRPCURLs),
		sink:         sink,
		cursors:      cursors,
		logger:       logger.With("chain", chain),
		pending:      make(map[string]pendingTransfer),
	}
}

func (w *Watcher) Chain() chains.Chain { return w.chain }

func (w *Watcher) RequiredConfirmations() int64 { return w.cfg.RequiredConfirms }

func (w *Watcher) SeedPending(_ context.Context, transfer chains.PendingTransfer) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, exists := w.pending[transfer.TxHash]; !exists {
		w.pending[transfer.TxHash] = pendingTransfer{amount: transfer.Amount, blockHeight: transfer.BlockHeight}
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
	latest, err := w.blockNumber(ctx)
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}

	if err := w.scanNewLogs(ctx, latest); err != nil {
		w.logger.Warn("scan new logs failed", "error", err)
	}

	w.refreshConfirmations(ctx, latest)
	return nil
}

func (w *Watcher) blockNumber(ctx context.Context) (int64, error) {
	var hexResult string
	if err := w.client.call(ctx, "eth_blockNumber", nil, &hexResult); err != nil {
		return 0, err
	}
	return parseHexQuantity(hexResult)
}

func (w *Watcher) scanNewLogs(ctx context.Context, latestBlock int64) error {
	fromBlock, _, err := w.cursors.GetCursor(ctx, w.chain)
	if err != nil {
		return fmt.Errorf("load cursor: %w", err)
	}
	if fromBlock == 0 {
		// First run: short lookback instead of scanning from genesis.
		fromBlock = latestBlock - 50
		if fromBlock < 0 {
			fromBlock = 0
		}
	} else {
		fromBlock++
	}

	if fromBlock > latestBlock {
		return nil
	}

	batch := w.cfg.LogsBatchBlocks
	if batch <= 0 {
		batch = 2000
	}

	cursor := fromBlock
	lastCompleted := fromBlock - 1
	for cursor <= latestBlock {
		to := cursor + batch - 1
		if to > latestBlock {
			to = latestBlock
		}

		params := []interface{}{map[string]interface{}{
			"fromBlock": toHexQuantity(cursor),
			"toBlock":   toHexQuantity(to),
			"address":   w.cfg.USDTContract,
			"topics":    []interface{}{transferTopic, nil, w.addressTopic},
		}}

		var logs []rpcLog
		if err := w.client.call(ctx, "eth_getLogs", params, &logs); err != nil {
			return fmt.Errorf("eth_getLogs [%d,%d]: %w", cursor, to, err)
		}

		for _, l := range logs {
			w.handleLog(l)
		}

		lastCompleted = to
		cursor = to + 1
	}

	if err := w.cursors.SetCursor(ctx, w.chain, lastCompleted, time.Now()); err != nil {
		return fmt.Errorf("save cursor: %w", err)
	}
	return nil
}

func (w *Watcher) handleLog(l rpcLog) {
	if len(l.Topics) < 3 {
		return
	}

	rawAmount, err := parseHexBigInt(l.Data)
	if err != nil {
		w.logger.Warn("skip log with unparseable amount", "tx", l.TransactionHash, "data", l.Data, "error", err)
		return
	}
	amount := decimal.NewFromBigInt(rawAmount, -usdtDecimals)

	blockHeight, err := parseHexQuantity(l.BlockNumber)
	if err != nil {
		w.logger.Warn("skip log with unparseable block number", "tx", l.TransactionHash, "error", err)
		return
	}

	w.mu.Lock()
	w.pending[l.TransactionHash] = pendingTransfer{amount: amount, blockHeight: blockHeight}
	w.mu.Unlock()
}

func (w *Watcher) refreshConfirmations(ctx context.Context, latestBlock int64) {
	type item struct {
		txHash      string
		amount      decimal.Decimal
		blockHeight int64
	}

	w.mu.Lock()
	items := make([]item, 0, len(w.pending))
	for tx, p := range w.pending {
		items = append(items, item{tx, p.amount, p.blockHeight})
	}
	w.mu.Unlock()

	for _, it := range items {
		w.emit(ctx, it.txHash, it.amount, it.blockHeight, latestBlock)

		confirmations := latestBlock - it.blockHeight + 1
		if confirmations >= w.cfg.RequiredConfirms {
			w.mu.Lock()
			delete(w.pending, it.txHash)
			w.mu.Unlock()
		}
	}
}

func (w *Watcher) emit(ctx context.Context, txHash string, amount decimal.Decimal, blockHeight, latestBlock int64) {
	confirmations := latestBlock - blockHeight + 1
	if confirmations < 0 {
		confirmations = 0
	}
	event := chains.TransferEvent{
		Chain:         w.chain,
		TxHash:        txHash,
		Amount:        amount,
		BlockHeight:   blockHeight,
		Confirmations: confirmations,
		ObservedAt:    time.Now(),
	}
	if err := w.sink.OnTransfer(ctx, event); err != nil {
		w.logger.Error("sink rejected transfer event", "tx", txHash, "error", err)
	}
}
