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
	StatusUnderpaid  Status = "underpaid"
	StatusExpired    Status = "expired"
)

type Order struct {
	ID                    uuid.UUID        `db:"id"`
	UserID                string           `db:"user_id"`
	Chain                 string           `db:"chain"`
	Token                 string           `db:"token"`
	BaseAmountUSD         decimal.Decimal  `db:"base_amount_usd"`
	ExpectedAmount        decimal.Decimal  `db:"expected_amount"`
	ReceivedAmount        *decimal.Decimal `db:"received_amount"`
	Status                Status           `db:"status"`
	TxHash                *string          `db:"tx_hash"`
	TxBlockHeight         *int64           `db:"tx_block_height"`
	Confirmations         int64            `db:"confirmations"`
	RequiredConfirmations int64            `db:"required_confirmations"`
	CreatedAt             time.Time        `db:"created_at"`
	ExpiresAt             time.Time        `db:"expires_at"`
	ConfirmedAt           *time.Time       `db:"confirmed_at"`
	UpdatedAt             time.Time        `db:"updated_at"`
}
