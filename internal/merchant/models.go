package merchant

import (
	"time"

	"github.com/google/uuid"
)

type Merchant struct {
	ID                     uuid.UUID `db:"id"`
	Name                   string    `db:"name"`
	APIKey                 string    `db:"api_key"`
	APISecretEncrypted     []byte    `db:"api_secret_encrypted"`
	WebhookURL             *string   `db:"webhook_url"`
	WebhookSecretEncrypted []byte    `db:"webhook_secret_encrypted"`
	IsActive               bool      `db:"is_active"`
	CreatedAt              time.Time `db:"created_at"`
	UpdatedAt              time.Time `db:"updated_at"`
}
