# Frenix Pay

A standalone, self-hosted crypto payment gateway for accepting USDT
deposits across TRON, Ethereum, BSC and Polygon, with fully automated
on-chain detection and confirmation. It is not part of frenix-back-v3 or
any existing gateway — it's a separate service that other Frenix products
call over HTTP. No third-party payment processor, no Cloudflare
dependency; it runs as its own process on your VPS.

## How it's put together

```
cmd/frenixpay          entrypoint: wires everything, runs the HTTP server,
                        chain watchers, expiry job and webhook dispatcher
internal/
  api/                  chi router, handlers, HMAC auth + rate limiting
  audit/                append-only log of every detected/confirmed tx
  chains/                the ChainWatcher interface + Manager
    tron/                reference implementation: TronGrid polling
    evm/                 shared implementation for Ethereum/BSC/Polygon
  config/                env-driven configuration
  db/                    Postgres + Redis wiring, migrations, cursor store
  merchant/              API key/HMAC auth, webhook secret issuance
  orders/                order lifecycle: create, confirm, expire
  ratelimit/             Redis-backed fixed-window limiter
  security/              AES-GCM encryption for merchant secrets at rest
  wallet/                watch-only BIP44 address derivation
  webhook/               HMAC-signed outbound delivery with retry
migrations/              golang-migrate SQL schema
```

## Design choices

**Postgres**, via sqlx. Orders are a financial ledger: exact `NUMERIC`
amounts, `SELECT`-free atomic updates via `UPDATE ... WHERE ... RETURNING`
for idempotent confirmation handling, partial/unique indexes (one
unwatched address per order, one order per `(chain, tx_hash)` ever), and
JSONB for the audit trail. golang-migrate and sqlx both have first-class
Postgres support.

**Redis** is a performance layer only — the public API rate limiter and,
if you extend it, a hot cache in front of Postgres. Postgres is always
the durable source of truth for chain cursors and order state, so a
flushed Redis instance can never cause a missed or double-processed
deposit.

**Chain watchers are targeted, not full block scans.** TRON polls
TronGrid's contract-events API filtered to the USDT contract's `Transfer`
event; EVM chains use `eth_getLogs` filtered to the USDT contract and the
current set of watched recipient addresses (passed as an OR-matched
topic filter). Neither ever iterates every transaction in every block.

**Watch-only wallet.** This service is handed extended *public* keys
only — one for TRON (`m/44'/195'/0'/0`), one shared by every EVM chain
(`m/44'/60'/0'/0`, since the same secp256k1 pubkey→address transform
applies to all of them). It derives a brand-new, never-reused address per
order and can reconstruct every deposit address it has ever issued — and
can never move a single unit of the funds sent to them, because it never
holds a private key. `internal/wallet.New` rejects an `xprv` outright if
one is ever configured by mistake.

## Order lifecycle

1. `POST /v1/orders` (merchant-authenticated) allocates the next unused
   BIP44 index — atomically, from a Postgres sequence table, per wallet
   family (`tron` / `evm`, the latter shared across EVM chains so an
   address is never reused across chains either) — derives the deposit
   address, and starts watching it.
2. Every chain watcher reports deposits through the same
   `chains.DepositSink` interface. `internal/orders.Repository.ApplyDeposit`
   is a single atomic `UPDATE` that claims the order on first sighting,
   only ever writes a non-decreasing confirmation count, and only matches
   while the order is still `pending`/`confirming` — so it's safe to call
   repeatedly, including with duplicate or out-of-order watcher events
   for the same `tx_hash`.
3. Crossing `required_confirmations` transitions the order to `confirmed`
   in that same statement, unwatches the address, and enqueues a
   signed webhook delivery.
4. A background job expires unpaid `pending` orders past their
   `expires_at` and stops watching their addresses.
5. On restart, `orders.Service.LoadActiveWatches` reloads every
   `pending`/`confirming` order's address into the relevant watcher (and
   re-seeds in-flight confirmation tracking for anything already
   `confirming`), so nothing is missed across a restart.

## Adding a new chain

Implement `chains.ChainWatcher` (see `internal/chains/watcher.go`) in a
new subpackage and register it in `cmd/frenixpay/main.go`. Nothing in
`internal/orders` or `internal/api` needs to change.

## Running locally

```bash
cp .env.example .env    # fill in APP_ENCRYPTION_KEY, MASTER_XPUB_TRON, etc.
make dev-up              # Postgres + Redis via docker compose
set -a && source .env && set +a
make build
./bin/frenixpay -create-merchant="Frenix Back V3"   # prints API key + secret once
make run
```

The binary applies pending migrations itself on every startup — `make
migrate-up`/`migrate-down` are only there for manual inspection or
rollback via the golang-migrate CLI.

## API

All request/response bodies are JSON. Merchant-authenticated endpoints
require three headers:

- `X-Frenix-Key`: the merchant's API key
- `X-Frenix-Timestamp`: unix seconds
- `X-Frenix-Signature`: `hex(HMAC-SHA256(api_secret, "{timestamp}.{method}.{path}.{body}"))`

A request is rejected if its timestamp is more than 5 minutes off, so a
captured signature can't be replayed indefinitely.

### `POST /v1/orders` (merchant-authenticated)

```json
{ "amount_usd": "25.00", "chain": "tron" }
```

`chain` is optional; omit it to let the service pick (TRON first, then
whichever EVM chain is enabled). Returns:

```json
{
  "order_id": "...", "chain": "tron", "token": "USDT",
  "deposit_address": "T...", "qr_string": "T...",
  "amount_usd": "25.00", "amount_token": "25.00",
  "status": "pending", "confirmations": 0, "required_confirmations": 20,
  "created_at": "...", "expires_at": "..."
}
```

### `GET /v1/orders/{order_id}` (public)

Intentionally unauthenticated — the paying customer's own browser polls
this directly, and an order ID is an unguessable random UUID, not a
sequential one. Same shape as the create response, updated live.

### `POST /v1/webhooks/register` (merchant-authenticated)

```json
{ "url": "https://your-service.example/frenixpay/webhook" }
```

Returns `{ "webhook_secret": "..." }` — shown once. Frenix Pay signs every
delivery to that URL the same way: `X-Frenix-Signature` /
`X-Frenix-Timestamp` headers, `hex(HMAC-SHA256(webhook_secret,
"{timestamp}.{body}"))`. Delivery is retried with exponential backoff
(persisted in Postgres, so it survives a restart) up to 8 attempts;
polling `GET /v1/orders/{order_id}` remains available as a fallback for
merchants who don't register a webhook, or whose endpoint is down past
the retry budget.

## Security notes

- The master seed/private keys never touch this service — only a
  watch-only xpub per wallet family, loaded from env.
- Merchant `api_secret` and `webhook_secret` are encrypted at rest
  (AES-256-GCM, keyed by `APP_ENCRYPTION_KEY`), not stored in plaintext.
- Nothing under `internal/wallet`, `internal/security` or `internal/config`
  logs a secret value — only derived addresses and non-secret metadata.
