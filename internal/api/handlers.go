package api

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/hiren223344/frpay/internal/chains"
	"github.com/hiren223344/frpay/internal/merchant"
	"github.com/hiren223344/frpay/internal/orders"
)

type Handlers struct {
	orders   *orders.Service
	merchant *merchant.Service
	chainMgr *chains.Manager
	logger   *slog.Logger
}

func NewHandlers(ordersSvc *orders.Service, merchantSvc *merchant.Service, chainMgr *chains.Manager, logger *slog.Logger) *Handlers {
	return &Handlers{orders: ordersSvc, merchant: merchantSvc, chainMgr: chainMgr, logger: logger}
}

// chainLabels gives each supported chain a customer-facing display name.
// Adding a new chain to internal/chains only requires an entry here for
// it to show up in the public chain list — no other API change needed.
var chainLabels = map[chains.Chain]string{
	chains.Tron:     "TRON (TRC20)",
	chains.Ethereum: "Ethereum (ERC20)",
	chains.BSC:      "BNB Smart Chain (BEP20)",
	chains.Polygon:  "Polygon",
}

// ListChains handles GET /v1/chains. Public: it's configuration, not a
// secret, and the checkout page needs it before any order (and thus any
// merchant auth) exists.
func (h *Handlers) ListChains(w http.ResponseWriter, r *http.Request) {
	out := make([]chainInfo, 0, len(h.chainMgr.Chains()))
	for _, c := range h.chainMgr.Chains() {
		required, _ := h.chainMgr.RequiredConfirmations(c)
		label := chainLabels[c]
		if label == "" {
			label = string(c)
		}
		out = append(out, chainInfo{
			Chain:                 string(c),
			Label:                 label,
			Token:                 "USDT",
			Decimals:              6,
			RequiredConfirmations: required,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// CreateOrder handles POST /v1/orders. Requires merchant auth (see
// authMiddleware); the calling merchant is taken from the request
// context, never from the request body.
func (h *Handlers) CreateOrder(w http.ResponseWriter, r *http.Request) {
	m := merchantFromContext(r.Context())
	if m == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req createOrderRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	amount, err := decimal.NewFromString(req.AmountUSD)
	if err != nil {
		writeError(w, http.StatusBadRequest, "amount_usd must be a valid decimal string")
		return
	}

	order, err := h.orders.CreateOrder(r.Context(), m.ID, amount, req.PreferredChain)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, toOrderResponse(order))
}

// GetOrder handles GET /v1/orders/{order_id}. This is intentionally
// public (no merchant auth): the paying frontend — the end customer's
// browser — polls it directly, and an order ID is an unguessable random
// UUID, not a sequential or otherwise enumerable identifier.
func (h *Handlers) GetOrder(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "orderID")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid order_id")
		return
	}

	order, err := h.orders.GetOrder(r.Context(), id)
	if errors.Is(err, orders.ErrNotFound) {
		writeError(w, http.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		h.logger.Error("get order failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, toOrderResponse(order))
}

// RegisterWebhook handles POST /v1/webhooks/register. Requires merchant
// auth; issues a fresh signing secret every time it's called, which
// invalidates any secret issued previously for this merchant.
func (h *Handlers) RegisterWebhook(w http.ResponseWriter, r *http.Request) {
	m := merchantFromContext(r.Context())
	if m == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req registerWebhookRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	parsed, err := url.Parse(req.URL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		writeError(w, http.StatusBadRequest, "url must be a valid http(s) URL")
		return
	}

	secret, err := h.merchant.RegisterWebhook(r.Context(), m.ID, req.URL)
	if err != nil {
		h.logger.Error("register webhook failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, registerWebhookResponse{WebhookSecret: secret})
}

func toOrderResponse(o *orders.Order) orderResponse {
	resp := orderResponse{
		OrderID:        o.ID.String(),
		Chain:          o.Chain,
		Token:          o.Token,
		DepositAddress: o.DerivedAddress,
		QRString:       o.DerivedAddress,
		AmountUSD:      o.AmountUSD.String(),
		AmountToken:    o.AmountToken.String(),
		Status:         string(o.Status),
		Confirmations:  o.Confirmations,
		RequiredConfs:  o.RequiredConfirmations,
		CreatedAt:      o.CreatedAt.UTC().Format(timeFormat),
		ExpiresAt:      o.ExpiresAt.UTC().Format(timeFormat),
	}
	if o.TxHash != nil {
		resp.TxHash = *o.TxHash
	}
	if o.ConfirmedAt != nil {
		resp.ConfirmedAt = o.ConfirmedAt.UTC().Format(timeFormat)
	}
	return resp
}

const timeFormat = "2006-01-02T15:04:05Z07:00"
