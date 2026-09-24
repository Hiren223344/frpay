package merchant

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hiren223344/frpay/internal/security"
)

const (
	apiKeyBytes        = 16
	apiSecretBytes     = 32
	webhookSecretBytes = 32
)

type Service struct {
	repo *Repository
	box  *security.Box
}

func NewService(repo *Repository, box *security.Box) *Service {
	return &Service{repo: repo, box: box}
}

// Authenticate looks up a merchant by API key and returns it along with
// its decrypted API secret, used by the request-signing middleware to
// verify the inbound HMAC signature.
func (s *Service) Authenticate(ctx context.Context, apiKey string) (*Merchant, string, error) {
	m, err := s.repo.GetByAPIKey(ctx, apiKey)
	if err != nil {
		return nil, "", err
	}
	secret, err := s.box.Open(m.APISecretEncrypted)
	if err != nil {
		return nil, "", fmt.Errorf("decrypt api secret: %w", err)
	}
	return m, string(secret), nil
}

// CreateMerchant provisions a new merchant and returns its API key and
// secret in plaintext — the only time the secret is ever visible; it is
// stored only in encrypted form from then on. Intended for operator use
// (CLI), not a public HTTP endpoint.
func (s *Service) CreateMerchant(ctx context.Context, name string) (apiKey, apiSecret string, err error) {
	apiKey, err = security.GenerateSecret(apiKeyBytes)
	if err != nil {
		return "", "", err
	}
	apiKey = "fp_live_" + apiKey

	apiSecret, err = security.GenerateSecret(apiSecretBytes)
	if err != nil {
		return "", "", err
	}

	sealed, err := s.box.Seal([]byte(apiSecret))
	if err != nil {
		return "", "", err
	}

	now := time.Now().UTC()
	m := &Merchant{
		ID:                 uuid.New(),
		Name:               name,
		APIKey:             apiKey,
		APISecretEncrypted: sealed,
		IsActive:           true,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := s.repo.Create(ctx, m); err != nil {
		return "", "", err
	}
	return apiKey, apiSecret, nil
}

// RegisterWebhook stores the merchant's callback URL and issues a fresh
// webhook signing secret, returned once so the merchant can configure
// their endpoint to verify Frenix Pay's outbound signature.
func (s *Service) RegisterWebhook(ctx context.Context, merchantID uuid.UUID, url string) (string, error) {
	secret, err := security.GenerateSecret(webhookSecretBytes)
	if err != nil {
		return "", err
	}
	sealed, err := s.box.Seal([]byte(secret))
	if err != nil {
		return "", err
	}
	if err := s.repo.SetWebhook(ctx, merchantID, url, sealed); err != nil {
		return "", err
	}
	return secret, nil
}

// DecryptWebhookSecret is used by the webhook dispatcher to sign
// outbound deliveries.
func (s *Service) DecryptWebhookSecret(m *Merchant) (string, error) {
	if len(m.WebhookSecretEncrypted) == 0 {
		return "", fmt.Errorf("merchant %s has no webhook registered", m.ID)
	}
	secret, err := s.box.Open(m.WebhookSecretEncrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt webhook secret: %w", err)
	}
	return string(secret), nil
}
