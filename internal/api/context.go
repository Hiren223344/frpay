package api

import (
	"context"

	"github.com/hiren223344/frpay/internal/merchant"
)

type ctxKey int

const merchantCtxKey ctxKey = iota

func withMerchant(ctx context.Context, m *merchant.Merchant) context.Context {
	return context.WithValue(ctx, merchantCtxKey, m)
}

func merchantFromContext(ctx context.Context) *merchant.Merchant {
	m, _ := ctx.Value(merchantCtxKey).(*merchant.Merchant)
	return m
}
