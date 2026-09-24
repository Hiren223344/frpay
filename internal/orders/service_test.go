// Integration tests for the amount-matching engine — the highest-risk
// new logic in the fixed-address/amount-matching redesign, since it
// can't be exercised by hitting real chain RPCs in most environments.
//
// Requires a real Postgres instance with migrations already applied.
// Set TEST_POSTGRES_DSN to run:
//
//	TEST_POSTGRES_DSN="postgres://frenixpay:frenixpay@127.0.0.1:5432/frenixpay?sslmode=disable" go test ./internal/orders/...
package orders

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/hiren223344/frpay/internal/audit"
	"github.com/hiren223344/frpay/internal/chains"
	"github.com/hiren223344/frpay/internal/db"
)

// fakeWatcher satisfies chains.ChainWatcher without touching any real
// network, just enough for chains.Manager to report it as "enabled" with
// a configured RequiredConfirmations.
type fakeWatcher struct {
	chain    chains.Chain
	required int64
}

func (f *fakeWatcher) Chain() chains.Chain                                       { return f.chain }
func (f *fakeWatcher) RequiredConfirmations() int64                              { return f.required }
func (f *fakeWatcher) SeedPending(context.Context, chains.PendingTransfer) error { return nil }
func (f *fakeWatcher) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping amount-matching integration tests")
	}

	sqlDB, err := db.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	if _, err := sqlDB.Exec(`TRUNCATE orders, review_items, audit_log, chain_cursors`); err != nil {
		t.Fatalf("truncate test db: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	chainMgr := chains.NewManager(logger, &fakeWatcher{chain: chains.Tron, required: 3})

	repo := NewRepository(sqlDB)
	auditLogger := audit.NewLogger(sqlDB)

	return NewService(
		repo, chainMgr, auditLogger, 20*time.Minute,
		decimal.NewFromFloat(0.02), // tolerance
		decimal.NewFromInt(50),     // underpayment max gap %
		1, 99,                      // offset range
		20, // max collision retries
		logger,
	)
}

func TestCreateOrder_ConcurrentSameAmountNeverCollide(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	const n = 25
	var wg sync.WaitGroup
	results := make([]*Order, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o, err := svc.CreateOrder(ctx, "user-concurrent", decimal.NewFromFloat(7.00), "tron")
			results[i], errs[i] = o, err
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("order %d: unexpected error: %v", i, err)
		}
		amt := results[i].ExpectedAmount.String()
		if seen[amt] {
			t.Fatalf("duplicate expected_amount %s allocated to two concurrent orders", amt)
		}
		seen[amt] = true
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct amounts, got %d", n, len(seen))
	}
}

func TestOnTransfer_ExactMatchThenConfirmIdempotently(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	order, err := svc.CreateOrder(ctx, "user-1", decimal.NewFromFloat(10.00), "tron")
	if err != nil {
		t.Fatalf("create order: %v", err)
	}

	// First sighting: 1 confirmation, well under the required 3.
	err = svc.OnTransfer(ctx, chains.TransferEvent{
		Chain: chains.Tron, TxHash: "tx-exact-1", Amount: order.ExpectedAmount,
		BlockHeight: 100, Confirmations: 1, ObservedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("first transfer event: %v", err)
	}
	got, err := svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if got.Status != StatusConfirming {
		t.Fatalf("status = %q, want %q", got.Status, StatusConfirming)
	}
	if got.Confirmations != 1 {
		t.Fatalf("confirmations = %d, want 1", got.Confirmations)
	}

	// Crosses the required threshold: should confirm.
	err = svc.OnTransfer(ctx, chains.TransferEvent{
		Chain: chains.Tron, TxHash: "tx-exact-1", Amount: order.ExpectedAmount,
		BlockHeight: 100, Confirmations: 3, ObservedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("second transfer event: %v", err)
	}
	got, err = svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if got.Status != StatusConfirmed {
		t.Fatalf("status = %q, want %q", got.Status, StatusConfirmed)
	}
	if got.ConfirmedAt == nil {
		t.Fatalf("confirmed_at is nil after confirmation")
	}
	confirmedAt := *got.ConfirmedAt

	// Idempotency: a stale duplicate/lower-confirmation event for the
	// same tx must not regress state or error.
	err = svc.OnTransfer(ctx, chains.TransferEvent{
		Chain: chains.Tron, TxHash: "tx-exact-1", Amount: order.ExpectedAmount,
		BlockHeight: 100, Confirmations: 2, ObservedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("duplicate transfer event: %v", err)
	}
	got, err = svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if got.Status != StatusConfirmed {
		t.Fatalf("status regressed to %q after duplicate event", got.Status)
	}
	if got.Confirmations != 3 {
		t.Fatalf("confirmations regressed to %d after duplicate event", got.Confirmations)
	}
	if !got.ConfirmedAt.Equal(confirmedAt) {
		t.Fatalf("confirmed_at changed on a duplicate event: %v -> %v", confirmedAt, *got.ConfirmedAt)
	}
}

func TestOnTransfer_ToleranceBoundary(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	order, err := svc.CreateOrder(ctx, "user-2", decimal.NewFromFloat(20.00), "tron")
	if err != nil {
		t.Fatalf("create order: %v", err)
	}

	// Just inside tolerance (0.02): should match.
	withinTolerance := order.ExpectedAmount.Sub(decimal.NewFromFloat(0.02))
	err = svc.OnTransfer(ctx, chains.TransferEvent{
		Chain: chains.Tron, TxHash: "tx-tolerance-in", Amount: withinTolerance,
		BlockHeight: 200, Confirmations: 1, ObservedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("transfer event: %v", err)
	}
	got, err := svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if got.Status != StatusConfirming {
		t.Fatalf("amount %s within tolerance of expected %s did not match: status=%q", withinTolerance, order.ExpectedAmount, got.Status)
	}
}

func TestOnTransfer_Underpayment(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	order, err := svc.CreateOrder(ctx, "user-3", decimal.NewFromFloat(10.00), "tron")
	if err != nil {
		t.Fatalf("create order: %v", err)
	}

	// 40% short: within the default 50% underpayment gap.
	shortAmount := order.ExpectedAmount.Mul(decimal.NewFromFloat(0.6))
	err = svc.OnTransfer(ctx, chains.TransferEvent{
		Chain: chains.Tron, TxHash: "tx-underpaid", Amount: shortAmount,
		BlockHeight: 300, Confirmations: 1, ObservedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("transfer event: %v", err)
	}

	got, err := svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if got.Status != StatusUnderpaid {
		t.Fatalf("status = %q, want %q", got.Status, StatusUnderpaid)
	}
	if got.ReceivedAmount == nil || !got.ReceivedAmount.Equal(shortAmount) {
		t.Fatalf("received_amount = %v, want %s", got.ReceivedAmount, shortAmount)
	}

	var reviewCount int
	if err := svc.repo.db.Get(&reviewCount, `
		SELECT COUNT(*) FROM review_items WHERE chain = 'tron' AND tx_hash = 'tx-underpaid' AND reason = 'underpaid'
	`); err != nil {
		t.Fatalf("query review_items: %v", err)
	}
	if reviewCount != 1 {
		t.Fatalf("expected exactly one underpaid review item, got %d", reviewCount)
	}
}

func TestOnTransfer_Unmatched(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// No pending order anywhere near this amount.
	amount := decimal.NewFromFloat(123456.78)
	err := svc.OnTransfer(ctx, chains.TransferEvent{
		Chain: chains.Tron, TxHash: "tx-unmatched", Amount: amount,
		BlockHeight: 400, Confirmations: 1, ObservedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("transfer event: %v", err)
	}

	var reviewCount int
	if err := svc.repo.db.Get(&reviewCount, `
		SELECT COUNT(*) FROM review_items WHERE chain = 'tron' AND tx_hash = 'tx-unmatched' AND reason = 'unmatched' AND related_order_id IS NULL
	`); err != nil {
		t.Fatalf("query review_items: %v", err)
	}
	if reviewCount != 1 {
		t.Fatalf("expected exactly one unmatched review item, got %d", reviewCount)
	}

	// Sending the exact same unmatched event again must not create a
	// second review row (log every detected transfer, but don't spam
	// the review queue with the same tx on every poll).
	if err := svc.OnTransfer(ctx, chains.TransferEvent{
		Chain: chains.Tron, TxHash: "tx-unmatched", Amount: amount,
		BlockHeight: 400, Confirmations: 2, ObservedAt: time.Now(),
	}); err != nil {
		t.Fatalf("duplicate transfer event: %v", err)
	}
	if err := svc.repo.db.Get(&reviewCount, `
		SELECT COUNT(*) FROM review_items WHERE chain = 'tron' AND tx_hash = 'tx-unmatched'
	`); err != nil {
		t.Fatalf("query review_items: %v", err)
	}
	if reviewCount != 1 {
		t.Fatalf("duplicate unmatched event created %d review rows, want 1", reviewCount)
	}
}
