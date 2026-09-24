// Package orders implements the order lifecycle: creating deposit
// addresses, applying watcher-reported deposits idempotently, and
// expiring unpaid orders. It is the single place where chain watchers
// (internal/chains) and the wallet (internal/wallet) meet the database.
package orders

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/hiren223344/frpay/internal/audit"
	"github.com/hiren223344/frpay/internal/chains"
	"github.com/hiren223344/frpay/internal/wallet"
)

var (
	minOrderUSD = decimal.NewFromInt(1)
	maxOrderUSD = decimal.NewFromInt(100000)
)

// chainInfo maps a supported chain to the wallet family and derivation
// sequence it draws addresses from. Every EVM chain shares the "evm"
// sequence (and the same xpub), so a derivation index is never reused
// across any of them, not just within one.
type chainInfo struct {
	family      wallet.ChainFamily
	sequenceKey string
}

var chainRegistry = map[chains.Chain]chainInfo{
	chains.Tron:     {wallet.FamilyTron, "tron"},
	chains.Ethereum: {wallet.FamilyEVM, "evm"},
	chains.BSC:      {wallet.FamilyEVM, "evm"},
	chains.Polygon:  {wallet.FamilyEVM, "evm"},
}

// defaultChainPriority is the order in which a chain is picked for an
// order that didn't request one. TRON first: it's the primary use case
// this service was built around.
var defaultChainPriority = []chains.Chain{chains.Tron, chains.Ethereum, chains.BSC, chains.Polygon}

// WebhookNotifier is implemented by internal/webhook.Dispatcher. Kept as
// a narrow interface here so the orders package doesn't depend on HTTP
// delivery details.
type WebhookNotifier interface {
	NotifyConfirmed(ctx context.Context, order *Order) error
}

type Service struct {
	repo        *Repository
	hd          *wallet.HDWallet
	chainMgr    *chains.Manager
	audit       *audit.Logger
	webhooks    WebhookNotifier
	orderExpiry time.Duration
	logger      *slog.Logger
}

func NewService(repo *Repository, hd *wallet.HDWallet, chainMgr *chains.Manager, auditLogger *audit.Logger, webhooks WebhookNotifier, orderExpiry time.Duration, logger *slog.Logger) *Service {
	return &Service{
		repo:        repo,
		hd:          hd,
		chainMgr:    chainMgr,
		audit:       auditLogger,
		webhooks:    webhooks,
		orderExpiry: orderExpiry,
		logger:      logger,
	}
}

// CreateOrder validates the requested amount, allocates a brand-new
// deposit address on the chosen chain, and starts watching it.
func (s *Service) CreateOrder(ctx context.Context, merchantID uuid.UUID, amountUSD decimal.Decimal, preferredChain string) (*Order, error) {
	if amountUSD.LessThan(minOrderUSD) || amountUSD.GreaterThan(maxOrderUSD) {
		return nil, fmt.Errorf("amount_usd must be between %s and %s", minOrderUSD, maxOrderUSD)
	}

	chain, err := s.resolveChain(preferredChain)
	if err != nil {
		return nil, err
	}
	info := chainRegistry[chain]

	index, err := s.repo.AllocateDerivationIndex(ctx, info.sequenceKey)
	if err != nil {
		return nil, err
	}
	address, err := s.hd.DeriveAddress(info.family, uint32(index))
	if err != nil {
		return nil, fmt.Errorf("derive deposit address: %w", err)
	}

	requiredConfirmations, _ := s.chainMgr.RequiredConfirmations(chain)

	now := time.Now().UTC()
	order := &Order{
		ID:                    uuid.New(),
		MerchantID:            merchantID,
		Chain:                 string(chain),
		Token:                 "USDT",
		DerivedAddress:        address,
		DerivationIndex:       index,
		AmountUSD:             amountUSD,
		AmountToken:           amountUSD, // USDT is treated as a 1:1 USD peg
		RateUSDPerToken:       decimal.NewFromInt(1),
		Status:                StatusPending,
		RequiredConfirmations: requiredConfirmations,
		CreatedAt:             now,
		ExpiresAt:             now.Add(s.orderExpiry),
		UpdatedAt:             now,
	}

	if err := s.repo.CreateOrder(ctx, order); err != nil {
		return nil, err
	}

	if err := s.chainMgr.Watch(ctx, chain, address); err != nil {
		s.logger.Error("failed to start watching new order's address; it will be picked up on next reconciliation", "order_id", order.ID, "error", err)
	}

	_ = s.audit.Log(ctx, &order.ID, string(chain), "", "order_created", map[string]interface{}{
		"amount_usd": amountUSD.String(),
		"address":    address,
	})

	return order, nil
}

func (s *Service) resolveChain(preferred string) (chains.Chain, error) {
	if preferred != "" {
		c := chains.Chain(strings.ToLower(preferred))
		if _, known := chainRegistry[c]; !known {
			return "", fmt.Errorf("unsupported chain %q", preferred)
		}
		if _, enabled := s.chainMgr.WatcherFor(c); !enabled {
			return "", fmt.Errorf("chain %q is not currently enabled", preferred)
		}
		return c, nil
	}
	for _, c := range defaultChainPriority {
		if _, enabled := s.chainMgr.WatcherFor(c); enabled {
			return c, nil
		}
	}
	return "", fmt.Errorf("no chains are currently enabled")
}

func (s *Service) GetOrder(ctx context.Context, id uuid.UUID) (*Order, error) {
	return s.repo.GetOrder(ctx, id)
}

// OnDeposit implements chains.DepositSink. It is called by every chain
// watcher, at least once per detected transaction and again on every
// poll while it accumulates confirmations, so it must be — and is —
// idempotent: internal/orders/repository.go's ApplyDeposit only ever
// writes a strictly non-decreasing confirmation count and only while an
// order is still pending/confirming.
func (s *Service) OnDeposit(ctx context.Context, event chains.DepositEvent) error {
	amountDisplay := "unknown"
	if event.Amount != nil {
		amountDisplay = decimal.NewFromBigInt(event.Amount, -event.TokenDecimals).String()
	}

	_ = s.audit.Log(ctx, nil, string(event.Chain), event.TxHash, "deposit_observed", map[string]interface{}{
		"address":       event.ToAddress,
		"amount":        amountDisplay,
		"confirmations": event.Confirmations,
		"block_height":  event.BlockHeight,
	})

	order, err := s.repo.ApplyDeposit(ctx, string(event.Chain), event.ToAddress, event.TxHash, event.BlockHeight, event.Confirmations)
	if err != nil {
		return err
	}
	if order == nil {
		// No active order matched (already confirmed/expired, or a stale
		// duplicate event). Already captured in the audit trail above.
		return nil
	}

	_ = s.audit.Log(ctx, &order.ID, string(event.Chain), event.TxHash, "order_deposit_updated", map[string]interface{}{
		"status":        order.Status,
		"confirmations": order.Confirmations,
	})

	if order.Status == StatusConfirmed {
		_ = s.audit.Log(ctx, &order.ID, string(event.Chain), event.TxHash, "order_confirmed", map[string]interface{}{
			"amount": amountDisplay,
		})
		if err := s.chainMgr.Unwatch(ctx, event.Chain, event.ToAddress); err != nil {
			s.logger.Warn("failed to unwatch confirmed order's address", "order_id", order.ID, "error", err)
		}
		if s.webhooks != nil {
			if err := s.webhooks.NotifyConfirmed(ctx, order); err != nil {
				s.logger.Error("failed to enqueue confirmation webhook", "order_id", order.ID, "error", err)
			}
		}
	}

	return nil
}

// RunExpiryLoop periodically expires unpaid orders and stops watching
// their addresses, until ctx is cancelled.
func (s *Service) RunExpiryLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.expireOnce(ctx)
		}
	}
}

func (s *Service) expireOnce(ctx context.Context) {
	expired, err := s.repo.ExpirePastDue(ctx)
	if err != nil {
		s.logger.Error("expire past-due orders failed", "error", err)
		return
	}
	for _, o := range expired {
		if err := s.chainMgr.Unwatch(ctx, chains.Chain(o.Chain), o.DerivedAddress); err != nil {
			s.logger.Warn("failed to unwatch expired order's address", "order_id", o.ID, "error", err)
		}
		_ = s.audit.Log(ctx, &o.ID, o.Chain, "", "order_expired", nil)
	}
}

// LoadActiveWatches reconstructs every chain watcher's in-memory watch
// set from the database on startup, so a restart never misses a deposit
// to an order created before the restart. Orders already in "confirming"
// have their in-flight tx seeded too, so confirmation counting resumes
// without re-discovering the transaction from scratch.
func (s *Service) LoadActiveWatches(ctx context.Context) error {
	for _, chain := range s.chainMgr.Chains() {
		active, err := s.repo.ListActive(ctx, string(chain))
		if err != nil {
			return fmt.Errorf("load active orders for %s: %w", chain, err)
		}
		for _, o := range active {
			if err := s.chainMgr.Watch(ctx, chain, o.DerivedAddress); err != nil {
				s.logger.Error("failed to reload watch on startup", "order_id", o.ID, "error", err)
				continue
			}
			if o.Status == StatusConfirming && o.TxHash != nil {
				blockHeight := int64(0)
				if o.TxBlockHeight != nil {
					blockHeight = *o.TxBlockHeight
				}
				if err := s.chainMgr.SeedConfirming(ctx, chain, o.DerivedAddress, *o.TxHash, blockHeight); err != nil {
					s.logger.Error("failed to seed in-flight confirmation on startup", "order_id", o.ID, "error", err)
				}
			}
		}
		s.logger.Info("reloaded active watches", "chain", chain, "count", len(active))
	}
	return nil
}
