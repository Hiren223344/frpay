// Package ton implements chains.ChainWatcher for TON, detecting USDT
// Jetton transfers to a single fixed owner address via toncenter.com's
// v3 API, which resolves the underlying Jetton-wallet bookkeeping and
// returns decoded transfers filtered directly by owner_address and
// jetton_master — no manual cell/BOC parsing needed.
//
// TON's public API surface is less stable than TRON's or an EVM chain's
// JSON-RPC, and this watcher was written without the ability to verify
// live request/response shapes against toncenter (no network access in
// the environment this was built in). The endpoint path and field names
// in types.go match toncenter's documented v3 jetton/transfers API as of
// this writing; verify against a real testnet transfer before relying on
// this in production, and adjust types.go if the shape has moved on.
package ton

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/hiren223344/frpay/internal/chains"
	"github.com/hiren223344/frpay/internal/config"
)

const usdtDecimals = 6

// assumedBlockTimeSeconds approximates TON's masterchain block interval,
// used to turn "confirmations" (a concept borrowed from chains with
// discrete blocks) into an elapsed-time proxy: TON's actual finality
// model is per-shard/masterchain reference, not a simple block count,
// and toncenter's jetton transfers endpoint doesn't expose a per-transfer
// masterchain seqno to correlate against directly.
const assumedBlockTimeSeconds = 5

type pendingTransfer struct {
	amount   decimal.Decimal
	lt       int64 // transaction_lt: TON's per-account logical time, used as the block-height analog
	observed int64 // transaction_now, unix seconds, used for the confirmation-time proxy
}

type Watcher struct {
	cfg     config.TONConfig
	client  *client
	sink    chains.TransferSink
	cursors chains.CursorStore
	logger  *slog.Logger

	mu      sync.Mutex
	pending map[string]pendingTransfer // txHash -> transfer info
}

func NewWatcher(cfg config.TONConfig, sink chains.TransferSink, cursors chains.CursorStore, logger *slog.Logger) *Watcher {
	return &Watcher{
		cfg:     cfg,
		client:  newClient(cfg.APIURL, cfg.FallbackAPIURLs, cfg.APIKey),
		sink:    sink,
		cursors: cursors,
		logger:  logger.With("chain", "ton"),
		pending: make(map[string]pendingTransfer),
	}
}

func (w *Watcher) Chain() chains.Chain { return chains.TON }

func (w *Watcher) RequiredConfirmations() int64 { return w.cfg.RequiredConfirms }

// SeedPending reseeds a transfer's confirmation tracking after a
// restart. The elapsed-time confirmation proxy restarts from "now"
// rather than the transfer's real observed time (which PendingTransfer
// doesn't carry) — conservative: it only ever makes an already-old
// transfer wait a little longer, never credits it early.
func (w *Watcher) SeedPending(_ context.Context, transfer chains.PendingTransfer) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, exists := w.pending[transfer.TxHash]; !exists {
		w.pending[transfer.TxHash] = pendingTransfer{amount: transfer.Amount, lt: transfer.BlockHeight, observed: time.Now().Unix()}
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
	now := time.Now().Unix()

	if err := w.scanNewTransfers(ctx); err != nil {
		w.logger.Warn("scan new transfers failed", "error", err)
	}

	w.refreshConfirmations(ctx, now)
	return nil
}

const maxPagesPerPoll = 20
const transfersPageLimit = 100

func (w *Watcher) scanNewTransfers(ctx context.Context) error {
	_, lastScannedAt, err := w.cursors.GetCursor(ctx, chains.TON)
	if err != nil {
		return fmt.Errorf("load cursor: %w", err)
	}

	startUtime := lastScannedAt.Unix() + 1
	if lastScannedAt.IsZero() {
		// First run: short lookback instead of scanning full history.
		startUtime = time.Now().Add(-10 * time.Minute).Unix()
	}

	maxLtSeen := int64(0)
	maxUtimeSeen := int64(0)
	offset := 0

	for page := 0; page < maxPagesPerPoll; page++ {
		query := url.Values{}
		query.Set("owner_address", w.cfg.Address)
		query.Set("jetton_master", w.cfg.USDTJettonMaster)
		query.Set("direction", "in")
		query.Set("start_utime", strconv.FormatInt(startUtime, 10))
		query.Set("limit", strconv.Itoa(transfersPageLimit))
		query.Set("offset", strconv.Itoa(offset))
		query.Set("sort", "asc")

		var resp jettonTransfersResponse
		if err := w.client.get(ctx, "/jetton/transfers", query, &resp); err != nil {
			return fmt.Errorf("fetch jetton transfers: %w", err)
		}

		for _, tr := range resp.JettonTransfers {
			w.handleTransfer(tr)
			if lt, err := strconv.ParseInt(tr.TransactionLt, 10, 64); err == nil && lt > maxLtSeen {
				maxLtSeen = lt
			}
			if tr.TransactionNow > maxUtimeSeen {
				maxUtimeSeen = tr.TransactionNow
			}
		}

		if len(resp.JettonTransfers) < transfersPageLimit {
			break
		}
		offset += transfersPageLimit
	}

	if maxUtimeSeen > 0 {
		if err := w.cursors.SetCursor(ctx, chains.TON, maxLtSeen, time.Unix(maxUtimeSeen, 0)); err != nil {
			return fmt.Errorf("save cursor: %w", err)
		}
	}
	return nil
}

func (w *Watcher) handleTransfer(tr jettonTransfer) {
	rawAmount, err := decimal.NewFromString(tr.Amount)
	if err != nil {
		w.logger.Warn("skip transfer with unparseable amount", "tx", tr.TransactionHash, "amount", tr.Amount)
		return
	}
	amount := rawAmount.Shift(-usdtDecimals)

	lt, err := strconv.ParseInt(tr.TransactionLt, 10, 64)
	if err != nil {
		w.logger.Warn("skip transfer with unparseable transaction_lt", "tx", tr.TransactionHash, "lt", tr.TransactionLt)
		return
	}

	w.mu.Lock()
	w.pending[tr.TransactionHash] = pendingTransfer{amount: amount, lt: lt, observed: tr.TransactionNow}
	w.mu.Unlock()
}

func (w *Watcher) refreshConfirmations(ctx context.Context, nowUnix int64) {
	type item struct {
		txHash   string
		amount   decimal.Decimal
		lt       int64
		observed int64
	}

	w.mu.Lock()
	items := make([]item, 0, len(w.pending))
	for tx, p := range w.pending {
		items = append(items, item{tx, p.amount, p.lt, p.observed})
	}
	w.mu.Unlock()

	for _, it := range items {
		confirmations := (nowUnix - it.observed) / assumedBlockTimeSeconds
		if confirmations < 0 {
			confirmations = 0
		}

		event := chains.TransferEvent{
			Chain:         chains.TON,
			TxHash:        it.txHash,
			Amount:        it.amount,
			BlockHeight:   it.lt,
			Confirmations: confirmations,
			ObservedAt:    time.Now(),
		}
		if err := w.sink.OnTransfer(ctx, event); err != nil {
			w.logger.Error("sink rejected transfer event", "tx", it.txHash, "error", err)
		}

		if confirmations >= w.cfg.RequiredConfirms {
			w.mu.Lock()
			delete(w.pending, it.txHash)
			w.mu.Unlock()
		}
	}
}
