# Frenix Pay

A standalone, self-hosted crypto payment gateway for accepting USDT
deposits across TRON, TON, Polygon and BSC, with fully automated
on-chain detection and confirmation. It is not part of frenix-back-v3 or
any existing gateway — it's a separate service that other Frenix
products call over HTTP. No third-party payment processor, no
Cloudflare dependency; it runs as its own process on your VPS.

**Full documentation:**
- [docs.md](docs.md) — API reference, matching algorithm, chain watcher
  model, configuration reference, manual-review queue, how to add a
  chain, testing
- [deploy.md](deploy.md) — VPS deployment: systemd, Postgres/Redis, TLS,
  firewall, backups

## Receiving address model

Every chain has **one fixed receiving address**, set once in config —
not a unique address generated per order. This service holds no key
material at all, not even a watch-only extended public key: it only
ever needs the plain address to watch.

Since every customer on a given chain pays the same address, orders are
distinguished by **amount**: `expected_amount` is `base_amount_usd` plus
a small random offset (e.g. `$10.0042`), unique among that chain's
currently-pending orders. See
[docs.md#how-matching-works](docs.md#how-matching-works) for the full
algorithm, including how underpayments and transfers that don't match
anything are handled — both are logged for manual review, never
silently dropped.

## How it's put together

```
cmd/frenixpay          entrypoint: wires everything, runs the HTTP server,
                        chain watchers and the order-expiry job
internal/
  api/                  chi router, handlers, API-key auth + rate limiting
  audit/                append-only log of every detected transfer
  chains/                the ChainWatcher interface + Manager
    tron/                reference implementation: TronGrid polling
    ton/                 toncenter.com v3 jetton-transfers polling
    evm/                 shared implementation for Polygon + BSC
  config/                env-driven configuration
  db/                    Postgres + Redis wiring, migrations, cursor store
  orders/                order lifecycle: create, match, confirm, expire
  ratelimit/             Redis-backed fixed-window limiter
migrations/              golang-migrate SQL schema
```

## Quickstart

```bash
cp .env.example .env    # fill in API_KEY and each chain's fixed address
set -a && source .env && set +a

go build -o bin/frenixpay ./cmd/frenixpay
./bin/frenixpay
```

The binary applies pending migrations itself on every startup. See
[deploy.md](deploy.md) for Postgres/Redis setup and running this for
real on a VPS.

```bash
curl http://localhost:8080/v1/chains

curl -X POST http://localhost:8080/v1/orders \
  -H "X-API-Key: $API_KEY" -H 'Content-Type: application/json' \
  -d '{"user_id":"user_123","base_amount_usd":"10.00","chain":"tron"}'
```

## Testing

```bash
go build ./... && go vet ./...

# Amount-matching integration tests need a real Postgres with migrations
# applied (see docs.md#testing) — they skip cleanly without one:
TEST_POSTGRES_DSN="postgres://user:pass@localhost:5432/frenixpay?sslmode=disable" \
  go test ./internal/orders/... -v
```

## Security notes

- No chain in this service is ever given a private key — only the
  public address to watch. `internal/chains/*` reads on-chain data only.
- `API_KEY` is the single credential guarding `POST /v1/orders`; it must
  never be exposed to a browser. `GET /v1/orders/{id}` and `GET
  /v1/chains` are intentionally public (see docs.md) for a customer-
  facing frontend to poll directly.
- The TON watcher's API shape and confirmation model were written
  without live access to verify against toncenter.com — see the caveat
  in [docs.md](docs.md#chain-watchers) before relying on it in
  production.
