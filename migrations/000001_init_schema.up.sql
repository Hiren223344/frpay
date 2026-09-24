-- Frenix Pay schema: fixed receiving address per chain, orders matched
-- by amount (not by a per-order address). This service never derives
-- addresses or holds any key material — it only reads public on-chain
-- data against addresses configured once in env.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE orders (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                 TEXT NOT NULL,
    chain                   TEXT NOT NULL,
    token                   TEXT NOT NULL DEFAULT 'USDT',
    base_amount_usd         NUMERIC(20,8) NOT NULL CHECK (base_amount_usd > 0),
    -- base_amount_usd plus a small random offset, unique among this
    -- chain's currently-pending orders, so concurrent orders on one
    -- fixed address are distinguishable by amount alone.
    expected_amount         NUMERIC(20,8) NOT NULL CHECK (expected_amount > 0),
    -- The amount actually observed on-chain, once a transfer claims or
    -- underpays this order. NULL until then.
    received_amount         NUMERIC(20,8),
    status                  TEXT NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending','confirming','confirmed','underpaid','expired')),
    tx_hash                 TEXT,
    tx_block_height         BIGINT,
    confirmations           INTEGER NOT NULL DEFAULT 0,
    required_confirmations  INTEGER NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at              TIMESTAMPTZ NOT NULL,
    confirmed_at            TIMESTAMPTZ,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_orders_user ON orders(user_id);

-- Enforces "no two pending orders on the same chain share an expected
-- amount" at the database level, and doubles as the index the
-- amount-matching query filters on (chain, status='pending'). Once an
-- order leaves 'pending' its expected_amount is free to be reused by a
-- future order.
CREATE UNIQUE INDEX idx_orders_chain_pending_amount
    ON orders(chain, expected_amount) WHERE status = 'pending';

-- A tx_hash may only ever be attributed to one order per chain, however
-- many times a watcher re-reports it while it accumulates confirmations.
CREATE UNIQUE INDEX idx_orders_chain_txhash
    ON orders(chain, tx_hash) WHERE tx_hash IS NOT NULL;

-- Tracks the last block/ledger position each chain watcher has fully
-- processed, so a restart resumes exactly where it left off instead of
-- re-scanning from genesis or missing the gap since shutdown.
CREATE TABLE chain_cursors (
    chain               TEXT PRIMARY KEY,
    last_scanned_height BIGINT NOT NULL DEFAULT 0,
    last_scanned_at     TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Full trail of every transfer any watcher ever detected, matched or
-- not — the audit record this is money, independent of order state.
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

-- The manual-review queue: a transfer that didn't match any pending
-- order's amount within tolerance ('unmatched'), or matched one but for
-- less than expected ('underpaid'). Real money was received in both
-- cases, so nothing here is ever silently dropped.
CREATE TABLE review_items (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chain             TEXT NOT NULL,
    tx_hash           TEXT NOT NULL,
    amount            NUMERIC(20,8) NOT NULL,
    reason            TEXT NOT NULL CHECK (reason IN ('unmatched','underpaid')),
    related_order_id  UUID REFERENCES orders(id),
    status            TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
    notes             TEXT,
    detected_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at       TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_review_items_chain_tx ON review_items(chain, tx_hash);
CREATE INDEX idx_review_items_open ON review_items(status) WHERE status = 'open';
