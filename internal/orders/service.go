// Package orders implements the order lifecycle: generating a unique
// expected_amount per order, matching incoming transfers to pending
// orders by amount, and expiring unpaid orders. It is the single place
// where chain watchers (internal/chains) and the database meet.
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
)

var minOrderUSD = decimal.NewFromFloat(0.01)
var maxOrderUSD = decimal.NewFromInt(1000000)

type Service struct {
	repo        *Repository
	chainMgr    *chains.Manager
	audit       *audit.Logger
	orderExpiry time.Duration

	tolerance                 decimal.Decimal
	underpaymentMaxGapPercent decimal.Decimal
	offsetMin, offsetMax      int
	maxCollisionRetries       int

	logger *slog.Logger
}

func NewService(
	repo *Repository,
	chainMgr *chains.Manager,
	auditLogger *audit.Logger,
	orderExpiry time.Duration,
	tolerance, underpaymentMaxGapPercent decimal.Decimal,
	offsetMin, offsetMax, maxCollisionRetries int,
	logger *slog.Logger,
) *Service {
	return &Service{
		repo:                      repo,
		chainMgr:                  chainMgr,
		audit:                     auditLogger,
		orderExpiry:               orderExpiry,
		tolerance:                 tolerance,
		underpaymentMaxGapPercent: underpaymentMaxGapPercent,
		offsetMin:                 offsetMin,
		offsetMax:                 offsetMax,
		maxCollisionRetries:       maxCollisionRetries,
		logger:                    logger,
	}
}

// CreateOrder validates the requested amount and allocates a new order
// with a unique expected_amount on the chosen chain.
func (s *Service) CreateOrder(ctx context.Context, userID string, baseAmountUSD decimal.Decimal, chainStr string) (*Order, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if baseAmountUSD.LessThan(minOrderUSD) || baseAmountUSD.GreaterThan(maxOrderUSD) {
		return nil, fmt.Errorf("base_amount_usd must be between %s and %s", minOrderUSD, maxOrderUSD)
	}
	if chainStr == "" {
		return nil, fmt.Errorf("chain is required")
	}

	chain := chains.Chain(strings.ToLower(chainStr))
	required, enabled := s.chainMgr.RequiredConfirmations(chain)
	if !enabled {
		return nil, fmt.Errorf("chain %q is not currently enabled", chainStr)
	}

	order, err := s.repo.CreateWithUniqueAmount(ctx, userID, string(chain), baseAmountUSD, required, s.orderExpiry, s.offsetMin, s.offsetMax, s.maxCollisionRetries)
	if err != nil {
		return nil, err
	}

	_ = s.audit.Log(ctx, &order.ID, string(chain), "", "order_created", map[string]interface{}{
		"user_id":         userID,
		"base_amount_usd": baseAmountUSD.String(),
		"expected_amount": order.ExpectedAmount.String(),
	})

	return order, nil
}

func (s *Service) GetOrder(ctx context.Context, id uuid.UUID) (*Order, error) {
	return s.repo.GetOrder(ctx, id)
}

// OnTransfer implements chains.TransferSink. It is called by every chain
// watcher, at least once per detected transfer and again on every poll
// while it accumulates confirmations, so every path here is written to
// be safe under duplicate or out-of-order delivery for the same
// (chain, tx_hash):
//
//  1. If this tx already claimed a "confirming" order, just advance its
//     confirmation count (internal/orders/repository.go's
//     UpdateConfirmations only ever writes a non-decreasing count).
//  2. Else if this tx was already attributed to some order in the past
//     (now confirmed, or already logged underpaid) there's nothing left
//     to do — a repeat event for an already-finalized tx is a no-op.
//  3. Else this is a genuinely new transfer: try to claim a pending
//     order whose expected_amount matches within tolerance.
//  4. Failing that, try to attribute it as an underpayment against the
//     closest pending order, if the shortfall is within the configured
//     gap.
//  5. Failing that, it doesn't correspond to anything we know about —
//     logged to the review queue, never silently dropped, since it's
//     still real money received.
func (s *Service) OnTransfer(ctx context.Context, event chains.TransferEvent) error {
	amountStr := event.Amount.String()
	_ = s.audit.Log(ctx, nil, string(event.Chain), event.TxHash, "transfer_observed", map[string]interface{}{
		"amount":        amountStr,
		"confirmations": event.Confirmations,
		"block_height":  event.BlockHeight,
	})

	order, err := s.repo.UpdateConfirmations(ctx, string(event.Chain), event.TxHash, event.BlockHeight, event.Confirmations)
	if err != nil {
		return err
	}
	if order != nil {
		s.logProgress(ctx, order, event)
		return nil
	}

	claimed, err := s.repo.TxAlreadyClaimed(ctx, string(event.Chain), event.TxHash)
	if err != nil {
		return err
	}
	if claimed {
		return nil // already confirmed or already logged underpaid; nothing to do
	}

	order, err = s.repo.ClaimExactMatch(ctx, string(event.Chain), event.TxHash, event.Amount, event.BlockHeight, event.Confirmations, s.tolerance)
	if err != nil {
		return err
	}
	if order != nil {
		_ = s.audit.Log(ctx, &order.ID, string(event.Chain), event.TxHash, "order_matched", map[string]interface{}{
			"expected_amount": order.ExpectedAmount.String(),
			"received_amount": amountStr,
		})
		s.logProgress(ctx, order, event)
		return nil
	}

	order, err = s.repo.ClaimUnderpaid(ctx, string(event.Chain), event.TxHash, event.Amount, event.BlockHeight, s.underpaymentMaxGapPercent)
	if err != nil {
		return err
	}
	if order != nil {
		_ = s.audit.Log(ctx, &order.ID, string(event.Chain), event.TxHash, "order_underpaid", map[string]interface{}{
			"expected_amount": order.ExpectedAmount.String(),
			"received_amount": amountStr,
		})
		if err := s.repo.LogReviewItem(ctx, string(event.Chain), event.TxHash, event.Amount, ReasonUnderpaid, &order.ID); err != nil {
			s.logger.Error("failed to log underpaid review item", "order_id", order.ID, "error", err)
		}
		s.logger.Warn("order underpaid", "order_id", order.ID, "chain", event.Chain, "expected", order.ExpectedAmount, "received", amountStr)
		return nil
	}

	_ = s.audit.Log(ctx, nil, string(event.Chain), event.TxHash, "transfer_unmatched", map[string]interface{}{
		"amount": amountStr,
	})
	if err := s.repo.LogReviewItem(ctx, string(event.Chain), event.TxHash, event.Amount, ReasonUnmatched, nil); err != nil {
		s.logger.Error("failed to log unmatched review item", "chain", event.Chain, "tx", event.TxHash, "error", err)
	}
	s.logger.Warn("unmatched transfer received", "chain", event.Chain, "tx", event.TxHash, "amount", amountStr)
	return nil
}

func (s *Service) logProgress(ctx context.Context, order *Order, event chains.TransferEvent) {
	_ = s.audit.Log(ctx, &order.ID, string(event.Chain), event.TxHash, "order_confirmation_progress", map[string]interface{}{
		"status":        order.Status,
		"confirmations": order.Confirmations,
	})
	if order.Status == StatusConfirmed {
		_ = s.audit.Log(ctx, &order.ID, string(event.Chain), event.TxHash, "order_confirmed", map[string]interface{}{
			"received_amount": event.Amount.String(),
		})
	}
}

// RunExpiryLoop periodically expires unpaid orders until ctx is
// cancelled.
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
		_ = s.audit.Log(ctx, &o.ID, o.Chain, "", "order_expired", nil)
	}
}

// LoadPendingTransfers reseeds every chain watcher's in-flight
// confirmation tracking from orders still "confirming" in the database,
// so a restart never stalls an order that had already been detected but
// not yet finalized before shutdown.
func (s *Service) LoadPendingTransfers(ctx context.Context) error {
	for _, chain := range s.chainMgr.Chains() {
		confirming, err := s.repo.ListConfirming(ctx, string(chain))
		if err != nil {
			return fmt.Errorf("list confirming orders for %s: %w", chain, err)
		}
		for _, o := range confirming {
			if o.TxHash == nil || o.ReceivedAmount == nil {
				continue
			}
			blockHeight := int64(0)
			if o.TxBlockHeight != nil {
				blockHeight = *o.TxBlockHeight
			}
			transfer := chains.PendingTransfer{
				TxHash:      *o.TxHash,
				Amount:      *o.ReceivedAmount,
				BlockHeight: blockHeight,
			}
			if err := s.chainMgr.SeedPending(ctx, chain, transfer); err != nil {
				s.logger.Error("failed to reseed pending transfer on startup", "order_id", o.ID, "error", err)
			}
		}
		s.logger.Info("reseeded pending transfers", "chain", chain, "count", len(confirming))
	}
	return nil
}
