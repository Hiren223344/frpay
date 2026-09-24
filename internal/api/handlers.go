package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/hiren223344/frpay/internal/chains"
	"github.com/hiren223344/frpay/internal/orders"
)

type Handlers struct {
	orders    *orders.Service
	chainMgr  *chains.Manager
	addresses map[chains.Chain]string
	logger    *slog.Logger
}

func NewHandlers(ordersSvc *orders.Service, chainMgr *chains.Manager, addresses map[chains.Chain]string, logger *slog.Logger) *Handlers {
	return &Handlers{orders: ordersSvc, chainMgr: chainMgr, addresses: addresses, logger: logger}
}

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ListChains handles GET /v1/chains. Public: these are the fixed
// receiving addresses, not a secret, and a client needs them before it
// can even show a "choose a network" step.
func (h *Handlers) ListChains(w http.ResponseWriter, r *http.Request) {
	enabled := h.chainMgr.Chains()
	out := make([]chainInfo, 0, len(enabled))
	for _, c := range enabled {
		required, _ := h.chainMgr.RequiredConfirmations(c)
		out = append(out, chainInfo{
			Chain:                 string(c),
			Address:               h.addresses[c],
			Token:                 "USDT",
			Decimals:              6,
			RequiredConfirmations: required,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateOrder handles POST /v1/orders. Guarded by apiKeyMiddleware.
func (h *Handlers) CreateOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	amount, err := decimal.NewFromString(req.BaseAmountUSD)
	if err != nil {
		writeError(w, http.StatusBadRequest, "base_amount_usd must be a valid decimal string")
		return
	}

	order, err := h.orders.CreateOrder(r.Context(), req.UserID, amount, req.Chain)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, h.toOrderResponse(order))
}

// GetOrder handles GET /v1/orders/{order_id}. Public: the paying
// frontend polls it directly, and an order ID is an unguessable random
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

	writeJSON(w, http.StatusOK, h.toOrderResponse(order))
}

func (h *Handlers) toOrderResponse(o *orders.Order) orderResponse {
	resp := orderResponse{
		OrderID:        o.ID.String(),
		UserID:         o.UserID,
		Chain:          o.Chain,
		Token:          o.Token,
		Address:        h.addresses[chains.Chain(o.Chain)],
		QRString:       h.addresses[chains.Chain(o.Chain)],
		BaseAmountUSD:  o.BaseAmountUSD.String(),
		ExpectedAmount: o.ExpectedAmount.String(),
		Status:         string(o.Status),
		Confirmations:  o.Confirmations,
		RequiredConfs:  o.RequiredConfirmations,
		CreatedAt:      o.CreatedAt.UTC().Format(timeFormat),
		ExpiresAt:      o.ExpiresAt.UTC().Format(timeFormat),
	}
	if o.ReceivedAmount != nil {
		resp.ReceivedAmount = o.ReceivedAmount.String()
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
