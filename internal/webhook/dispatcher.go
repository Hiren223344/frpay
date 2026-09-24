// Package webhook delivers HMAC-signed order-confirmation callbacks to
// merchants, with persisted retry state so delivery survives a process
// restart. Polling GET /v1/orders/{id} remains available as a fallback
// for merchants who never register a webhook, or whose endpoint is down
// past the retry budget.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/hiren223344/frpay/internal/merchant"
	"github.com/hiren223344/frpay/internal/orders"
)

type payload struct {
	OrderID        string `json:"order_id"`
	MerchantID     string `json:"merchant_id"`
	Chain          string `json:"chain"`
	Token          string `json:"token"`
	DerivedAddress string `json:"deposit_address"`
	AmountUSD      string `json:"amount_usd"`
	AmountToken    string `json:"amount_token"`
	Status         string `json:"status"`
	TxHash         string `json:"tx_hash"`
	Confirmations  int64  `json:"confirmations"`
	ConfirmedAt    string `json:"confirmed_at"`
}

type Dispatcher struct {
	repo         *Repository
	merchantRepo *merchant.Repository
	merchantSvc  *merchant.Service
	httpClient   *http.Client
	logger       *slog.Logger
}

func NewDispatcher(repo *Repository, merchantRepo *merchant.Repository, merchantSvc *merchant.Service, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{
		repo:         repo,
		merchantRepo: merchantRepo,
		merchantSvc:  merchantSvc,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		logger:       logger,
	}
}

// NotifyConfirmed implements orders.WebhookNotifier. It persists a
// delivery record and makes one immediate best-effort attempt; any
// failure is left for RunDeliveryLoop to retry with backoff, so this
// never blocks or fails order confirmation itself.
func (d *Dispatcher) NotifyConfirmed(ctx context.Context, order *orders.Order) error {
	m, err := d.merchantRepo.GetByID(ctx, order.MerchantID)
	if err != nil {
		return fmt.Errorf("load merchant: %w", err)
	}
	if m.WebhookURL == nil || *m.WebhookURL == "" {
		return nil // no webhook registered; merchant relies on polling
	}

	txHash := ""
	if order.TxHash != nil {
		txHash = *order.TxHash
	}
	confirmedAt := ""
	if order.ConfirmedAt != nil {
		confirmedAt = order.ConfirmedAt.UTC().Format(time.RFC3339)
	}

	body, err := json.Marshal(payload{
		OrderID:        order.ID.String(),
		MerchantID:     order.MerchantID.String(),
		Chain:          order.Chain,
		Token:          order.Token,
		DerivedAddress: order.DerivedAddress,
		AmountUSD:      order.AmountUSD.String(),
		AmountToken:    order.AmountToken.String(),
		Status:         string(order.Status),
		TxHash:         txHash,
		Confirmations:  order.Confirmations,
		ConfirmedAt:    confirmedAt,
	})
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	secret, err := d.merchantSvc.DecryptWebhookSecret(m)
	if err != nil {
		return fmt.Errorf("decrypt webhook secret: %w", err)
	}
	timestamp := time.Now().Unix()
	signature := Sign(secret, timestamp, body)

	delivery := &Delivery{
		ID:            uuid.New(),
		OrderID:       order.ID,
		MerchantID:    order.MerchantID,
		URL:           *m.WebhookURL,
		Payload:       body,
		Signature:     signature,
		Status:        "pending",
		NextAttemptAt: time.Now(),
		CreatedAt:     time.Now(),
	}
	if err := d.repo.Create(ctx, delivery); err != nil {
		return fmt.Errorf("persist webhook delivery: %w", err)
	}

	d.attempt(ctx, delivery, timestamp)
	return nil
}

// RunDeliveryLoop retries pending/failed deliveries until ctx is
// cancelled. It is the sole delivery path after a restart, so an
// in-flight retry is never lost.
func (d *Dispatcher) RunDeliveryLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.retryDue(ctx)
		}
	}
}

func (d *Dispatcher) retryDue(ctx context.Context) {
	due, err := d.repo.ListDue(ctx, 50)
	if err != nil {
		d.logger.Error("list due webhook deliveries failed", "error", err)
		return
	}
	for i := range due {
		delivery := &due[i]
		timestamp := time.Now().Unix()

		m, err := d.merchantRepo.GetByID(ctx, delivery.MerchantID)
		if err != nil {
			d.logger.Error("load merchant for webhook retry failed", "delivery_id", delivery.ID, "error", err)
			continue
		}
		secret, err := d.merchantSvc.DecryptWebhookSecret(m)
		if err != nil {
			d.logger.Error("decrypt webhook secret for retry failed", "delivery_id", delivery.ID, "error", err)
			continue
		}
		delivery.Signature = Sign(secret, timestamp, delivery.Payload)
		d.attempt(ctx, delivery, timestamp)
	}
}

func (d *Dispatcher) attempt(ctx context.Context, delivery *Delivery, timestamp int64) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.URL, bytes.NewReader(delivery.Payload))
	if err != nil {
		d.logger.Error("build webhook request failed", "delivery_id", delivery.ID, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Frenix-Signature", delivery.Signature)
	req.Header.Set("X-Frenix-Timestamp", fmt.Sprintf("%d", timestamp))

	resp, err := d.httpClient.Do(req)
	if err != nil {
		d.scheduleRetry(ctx, delivery, 0, err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := d.repo.MarkDelivered(ctx, delivery.ID.String(), resp.StatusCode, string(respBody)); err != nil {
			d.logger.Error("mark webhook delivered failed", "delivery_id", delivery.ID, "error", err)
		}
		return
	}
	d.scheduleRetry(ctx, delivery, resp.StatusCode, string(respBody))
}

func (d *Dispatcher) scheduleRetry(ctx context.Context, delivery *Delivery, responseStatus int, responseBody string) {
	backoff := time.Duration(1<<uint(delivery.AttemptCount)) * 30 * time.Second
	if backoff > time.Hour {
		backoff = time.Hour
	}
	next := time.Now().Add(backoff)
	if err := d.repo.MarkAttemptFailed(ctx, delivery.ID.String(), responseStatus, responseBody, next); err != nil {
		d.logger.Error("mark webhook attempt failed", "delivery_id", delivery.ID, "error", err)
	}
}
