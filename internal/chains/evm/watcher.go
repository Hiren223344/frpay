// Package evm implements chains.ChainWatcher for EVM-compatible chains
// (Ethereum, BSC, Polygon, and any future EVM chain) with a single
// implementation parameterized by config.EVMConfig. It detects USDT
// deposits via eth_getLogs, filtered server-side to the USDT contract's
// Transfer topic and the current set of watched recipient addresses —
// never a full block scan.
package evm

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/hiren223344/frpay/internal/chains"
	"github.com/hiren223344/frpay/internal/config"
)

// transferTopic is keccak256("Transfer(address,address,uint256)"), the
// standard ERC20 Transfer event signature shared by every EVM chain.
const transferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

const usdtDecimals = 6 // true for USDT on Ethereum, BSC and Polygon

type pendingDeposit struct {
	address     string
	blockHeight int64
	amount      *big.Int
}

type Watcher struct {
	cfg     config.EVMConfig
	chain   chains.Chain
	client  *client
	sink    chains.DepositSink
	cursors chains.CursorStore
	logger  *slog.Logger

	mu      sync.RWMutex
	watched map[string]string // lowercase address -> canonical checksummed address
	pending map[string]pendingDeposit
}

func NewWatcher(chain chains.Chain, cfg config.EVMConfig, sink chains.DepositSink, cursors chains.CursorStore, logger *slog.Logger) *Watcher {
	return &Watcher{
		cfg:     cfg,
		chain:   chain,
		client:  newClient(cfg.RPCURL, cfg.FallbackRPCURLs),
		sink:    sink,
		cursors: cursors,
		logger:  logger.With("chain", chain),
		watched: make(map[string]string),
		pending: make(map[string]pendingDeposit),
	}
}

func (w *Watcher) Chain() chains.Chain { return w.chain }

func (w *Watcher) RequiredConfirmations() int64 { return w.cfg.RequiredConfirms }

func (w *Watcher) WatchAddress(_ context.Context, address string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.watched[strings.ToLower(address)] = address
	return nil
}

func (w *Watcher) UnwatchAddress(_ context.Context, address string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.watched, strings.ToLower(address))
	return nil
}

func (w *Watcher) SeedConfirming(_ context.Context, address, txHash string, blockHeight int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.watched[strings.ToLower(address)] = address
	if _, exists := w.pending[strings.ToLower(txHash)]; !exists {
		w.pending[strings.ToLower(txHash)] = pendingDeposit{address: address, blockHeight: blockHeight}
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
	w.mu.RLock()
	if len(w.watched) == 0 {
		w.mu.RUnlock()
		return nil
	}
	topicAddrs := make([]string, 0, len(w.watched))
	for lower := range w.watched {
		topicAddrs = append(topicAddrs, addressToTopic(lower))
	}
	w.mu.RUnlock()

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
			"topics":    []interface{}{transferTopic, nil, topicAddrs},
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
	toAddrLower := topicToAddress(l.Topics[2])

	w.mu.RLock()
	canonical, isWatched := w.watched[toAddrLower]
	w.mu.RUnlock()
	if !isWatched {
		return
	}

	amount, err := parseHexBigInt(l.Data)
	if err != nil {
		w.logger.Warn("skip log with unparseable amount", "tx", l.TransactionHash, "data", l.Data, "error", err)
		return
	}
	blockHeight, err := parseHexQuantity(l.BlockNumber)
	if err != nil {
		w.logger.Warn("skip log with unparseable block number", "tx", l.TransactionHash, "error", err)
		return
	}

	key := strings.ToLower(l.TransactionHash)
	w.mu.Lock()
	w.pending[key] = pendingDeposit{address: canonical, blockHeight: blockHeight, amount: amount}
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
		Chain:         w.chain,
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
