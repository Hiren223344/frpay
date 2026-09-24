package merchant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var ErrNotFound = errors.New("merchant not found")

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) GetByAPIKey(ctx context.Context, apiKey string) (*Merchant, error) {
	var m Merchant
	err := r.db.GetContext(ctx, &m, `SELECT * FROM merchants WHERE api_key = $1 AND is_active = true`, apiKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get merchant by api key: %w", err)
	}
	return &m, nil
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*Merchant, error) {
	var m Merchant
	err := r.db.GetContext(ctx, &m, `SELECT * FROM merchants WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get merchant by id: %w", err)
	}
	return &m, nil
}

func (r *Repository) Create(ctx context.Context, m *Merchant) error {
	_, err := r.db.NamedExecContext(ctx, `
		INSERT INTO merchants (id, name, api_key, api_secret_encrypted, is_active, created_at, updated_at)
		VALUES (:id, :name, :api_key, :api_secret_encrypted, :is_active, :created_at, :updated_at)
	`, m)
	if err != nil {
		return fmt.Errorf("insert merchant: %w", err)
	}
	return nil
}

func (r *Repository) SetWebhook(ctx context.Context, merchantID uuid.UUID, url string, secretEncrypted []byte) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE merchants SET webhook_url = $2, webhook_secret_encrypted = $3, updated_at = now()
		WHERE id = $1
	`, merchantID, url, secretEncrypted)
	if err != nil {
		return fmt.Errorf("set merchant webhook: %w", err)
	}
	return nil
}
