-- Frenix Pay initial schema.
-- This service is watch-only: it never stores private keys, only derived
-- watch addresses and derivation indices computed from a master xpub.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE merchants (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                    TEXT NOT NULL,
    api_key                 TEXT NOT NULL UNIQUE,
    api_secret_encrypted    BYTEA NOT NULL,
    webhook_url             TEXT,
    webhook_secret_encrypted BYTEA,
    is_active               BOOLEAN NOT NULL DEFAULT TRUE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Tracks the next unused BIP44 derivation index per wallet family, so
-- every order gets a brand-new address and none is ever reused. The key
-- is a wallet family ("tron", "evm"), not always a literal chain: every
-- EVM chain (ethereum/bsc/polygon) shares one xpub and therefore one
-- sequence, so an index (and the address it derives) is never handed out
-- twice across any EVM chain, not just within one.
CREATE TABLE chain_address_sequences (
    sequence_key TEXT PRIMARY KEY,
    next_index   BIGINT NOT NULL DEFAULT 0
);

-- Tracks the last block/ledger height each chain watcher has fully
-- processed, so a restart resumes exactly where it left off.
CREATE TABLE chain_cursors (
    chain               TEXT PRIMARY KEY,
    last_scanned_height BIGINT NOT NULL DEFAULT 0,
    last_scanned_at     TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE orders (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id             UUID NOT NULL REFERENCES merchants(id),
    chain                   TEXT NOT NULL,
    token                   TEXT NOT NULL DEFAULT 'USDT',
    derived_address         TEXT NOT NULL,
    derivation_index        BIGINT NOT NULL,
    amount_usd              NUMERIC(20,8) NOT NULL CHECK (amount_usd > 0),
    amount_token            NUMERIC(38,18) NOT NULL CHECK (amount_token > 0),
    rate_usd_per_token      NUMERIC(20,8) NOT NULL DEFAULT 1,
    status                  TEXT NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending','confirming','confirmed','expired','failed')),
    tx_hash                 TEXT,
    tx_block_height         BIGINT,
    confirmations           INTEGER NOT NULL DEFAULT 0,
    required_confirmations  INTEGER NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at              TIMESTAMPTZ NOT NULL,
    confirmed_at            TIMESTAMPTZ,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_orders_merchant ON orders(merchant_id);
CREATE INDEX idx_orders_watch ON orders(chain, status) WHERE status IN ('pending','confirming');
CREATE UNIQUE INDEX idx_orders_chain_address_index ON orders(chain, derivation_index);

-- A tx_hash may only ever be attributed to one order per chain. This is
-- the backstop that makes confirmation handling idempotent even if a
-- watcher event fires more than once for the same transaction.
CREATE UNIQUE INDEX idx_orders_chain_txhash ON orders(chain, tx_hash) WHERE tx_hash IS NOT NULL;

CREATE TABLE audit_log (
    id          BIGSERIAL PRIMARY KEY,
    order_id    UUID REFERENCES orders(id),
    chain       TEXT,
    tx_hash     TEXT,
    event_type  TEXT NOT NULL,
    details     JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_order ON audit_log(order_id);
CREATE INDEX idx_audit_log_tx ON audit_log(chain, tx_hash);

CREATE TABLE webhook_deliveries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id        UUID NOT NULL REFERENCES orders(id),
    merchant_id     UUID NOT NULL REFERENCES merchants(id),
    url             TEXT NOT NULL,
    payload         JSONB NOT NULL,
    signature       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','delivered','failed')),
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    response_status INTEGER,
    response_body   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_webhook_deliveries_pending ON webhook_deliveries(status, next_attempt_at)
    WHERE status IN ('pending','failed');
