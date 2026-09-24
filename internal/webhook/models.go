package webhook

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Delivery struct {
	ID             uuid.UUID       `db:"id"`
	OrderID        uuid.UUID       `db:"order_id"`
	MerchantID     uuid.UUID       `db:"merchant_id"`
	URL            string          `db:"url"`
	Payload        json.RawMessage `db:"payload"`
	Signature      string          `db:"signature"`
	Status         string          `db:"status"`
	AttemptCount   int             `db:"attempt_count"`
	LastAttemptAt  *time.Time      `db:"last_attempt_at"`
	NextAttemptAt  time.Time       `db:"next_attempt_at"`
	ResponseStatus *int            `db:"response_status"`
	ResponseBody   *string         `db:"response_body"`
	CreatedAt      time.Time       `db:"created_at"`
}
