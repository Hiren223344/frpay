package orders

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusConfirming Status = "confirming"
	StatusConfirmed  Status = "confirmed"
	StatusExpired    Status = "expired"
	StatusFailed     Status = "failed"
)

type Order struct {
	ID                    uuid.UUID       `db:"id"`
	MerchantID            uuid.UUID       `db:"merchant_id"`
	Chain                 string          `db:"chain"`
	Token                 string          `db:"token"`
	DerivedAddress        string          `db:"derived_address"`
	DerivationIndex       int64           `db:"derivation_index"`
	AmountUSD             decimal.Decimal `db:"amount_usd"`
	AmountToken           decimal.Decimal `db:"amount_token"`
	RateUSDPerToken       decimal.Decimal `db:"rate_usd_per_token"`
	Status                Status          `db:"status"`
	TxHash                *string         `db:"tx_hash"`
	TxBlockHeight         *int64          `db:"tx_block_height"`
	Confirmations         int64           `db:"confirmations"`
	RequiredConfirmations int64           `db:"required_confirmations"`
	CreatedAt             time.Time       `db:"created_at"`
	ExpiresAt             time.Time       `db:"expires_at"`
	ConfirmedAt           *time.Time      `db:"confirmed_at"`
	UpdatedAt             time.Time       `db:"updated_at"`
}
