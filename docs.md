# Frenix Pay — API & Architecture Reference

For installing and running the service on a VPS, see [deploy.md](deploy.md).
The top-level [README.md](README.md) has a quick local-dev quickstart.

## Contents

- [How matching works](#how-matching-works)
- [Order lifecycle](#order-lifecycle)
- [Chain watchers](#chain-watchers)
- [HTTP API](#http-api)
- [Configuration reference](#configuration-reference)
- [Manual review queue](#manual-review-queue)
- [Adding a new chain](#adding-a-new-chain)
- [Testing](#testing)

## How matching works

Every chain has exactly **one** receiving address, set once in config —
not a unique address per order. Since every customer paying on a given
chain sends to the same address, orders are distinguished by **amount**,
not by address.

When an order is created, its `expected_amount` is
`base_amount_usd` plus a small random offset (`$0.0001`–`$0.0099` by
default, configurable via `EXPECTED_AMOUNT_OFFSET_MIN`/`_MAX`). A
partial unique index on `(chain, expected_amount) WHERE status =
'pending'` guarantees no two orders pending on the same chain ever share
an amount; `POST /v1/orders` retries with a fresh offset on collision
(see `internal/orders/repository.go`'s `CreateWithUniqueAmount`) — this
was verified under real concurrent load (25 simultaneous requests for
the same `base_amount_usd` on the same chain, zero collisions; see
[Testing](#testing)).

When a watcher reports an incoming transfer (`chains.TransferEvent`),
`internal/orders.Service.OnTransfer` resolves it in this order:

1. **Already claimed, still confirming** — advance its confirmation
   count. Only ever writes a non-decreasing number, and only while the
   order is `confirming`, so repeated or out-of-order watcher events for
   the same `tx_hash` are safe.
2. **Already claimed, already finalized** — a repeat event for a tx
   that's `confirmed` or already logged `underpaid`. No-op.
3. **Exact match** — the tx amount is within `AMOUNT_TOLERANCE_USD`
   (default `$0.02`, for on-chain rounding) of some pending order's
   `expected_amount`. Claims the oldest such order atomically (`FOR
   UPDATE SKIP LOCKED`, so concurrent watcher polls can't double-claim).
4. **Underpayment** — no exact match, but the amount falls short of the
   *closest* pending order's `expected_amount` by no more than
   `UNDERPAYMENT_MAX_GAP_PERCENT` (default 50%). That order is marked
   `underpaid`, the transfer is logged to `review_items`, and it is
   **not** auto-credited.
5. **Unmatched** — doesn't correspond to anything pending. Logged to
   `review_items` with no `related_order_id`. Real money was received;
   nothing is ever silently dropped.

Every step also writes to `audit_log` regardless of outcome — that's the
full trail of every transfer any watcher has ever seen, independent of
order state.

## Order lifecycle

```
pending ──(exact match)──────────────► confirming ──(N confirmations)──► confirmed
   │                                                                          
   ├──(underpayment match)───────────► underpaid   (terminal; manual review)
   │
   └──(expires_at passes, still pending) ─────────► expired
```

- Only `pending` orders expire. One already `confirming` has a real
  transfer in flight and must run to completion regardless of the
  original expiry window.
- `underpaid` is terminal from this service's point of view — it does
  **not** auto-credit, and does not keep accumulating confirmations. A
  human (or a follow-up flow you build on top of `review_items`) decides
  whether to credit partially, ask the customer for the shortfall, or
  refund.
- A restart never loses in-flight state: `orders.Service.
  LoadPendingTransfers` reseeds every chain watcher's confirmation
  tracking from orders still `confirming` in the database on startup, so
  a transfer detected right before a crash still gets its confirmations
  counted afterward instead of stalling forever.

Crediting the user's actual Frenix account balance is **out of scope for
this service** — it is the source of truth for order status only.
Whatever calls `POST /v1/orders` is expected to poll (or otherwise watch)
`GET /v1/orders/{id}` and credit its own user's balance when it sees
`status: "confirmed"`.

## Chain watchers

One implementation per chain, all behind `chains.ChainWatcher`
(`internal/chains/watcher.go`). Each watcher continuously scans its
chain's **one** fixed address and reports every incoming USDT transfer —
matched or not — through `chains.TransferSink`, which
`internal/orders.Service` implements. Watchers never know about orders;
matching is entirely the orders package's job. That split is what makes
adding a new chain "implement this interface," full stop.

| Chain   | Package                    | Detection method                                                            | Confirmations                          |
|---------|-----------------------------|------------------------------------------------------------------------------|-----------------------------------------|
| TRON    | `internal/chains/tron`     | TronGrid contract-events API, filtered to the USDT contract's `Transfer` event | latest block − tx block, default 20    |
| TON     | `internal/chains/ton`      | toncenter.com v3 `/jetton/transfers`, filtered server-side by `owner_address` + `jetton_master` | elapsed time ÷ ~5s/block, default 5 (⚠ see below) |
| Polygon | `internal/chains/evm`      | `eth_getLogs` filtered to the USDT contract + this address's topic         | latest block − tx block, default 128   |
| BSC     | `internal/chains/evm`      | `eth_getLogs` filtered to the USDT contract + this address's topic         | latest block − tx block, default 15    |

None of them do a full block scan — every one filters server-side to
just the relevant contract/address before the watcher ever sees a log.

Every watcher: polls on its own goroutine at a configurable interval,
retries across a configurable primary + fallback RPC/API URL list with
exponential backoff, and persists its scan cursor
(`chain_cursors` table) so a restart resumes exactly where it left off.

**⚠ TON watcher caveat.** This was built without live network access to
verify toncenter's current API shape (see the comment at the top of
`internal/chains/ton/watcher.go`). The endpoint, parameters and field
names in `internal/chains/ton/types.go` match toncenter's documented v3
API as of when this was written. TON also doesn't have a simple
per-block confirmation count the way TRON/EVM chains do — the watcher
approximates "confirmations" as elapsed wall-clock time since the
transfer divided by an assumed ~5s block interval, which is
conservative (can only make a transfer wait *longer*, never credits it
early) but is an approximation, not a masterchain-seqno-verified count.
**Verify this against a real TON testnet transfer before relying on it
in production.**

## HTTP API

All request/response bodies are JSON.

### `POST /v1/orders`

Server-to-server only — guarded by a single shared secret
(`API_KEY`), sent as `X-API-Key`. **Never call this from a browser**;
the key would be exposed. Whatever calls this (frenix-back-v3, another
Frenix service) is the only thing that should hold it.

```json
// request
{ "user_id": "user_123", "base_amount_usd": "10.00", "chain": "tron" }
```

```json
// 201 response
{
  "order_id": "873c11c9-fbab-4b48-aed5-e517c1f88e38",
  "user_id": "user_123",
  "chain": "tron",
  "token": "USDT",
  "address": "TRp5otn82XoT37csQQTQjGyMB7o8646u6f",
  "qr_string": "TRp5otn82XoT37csQQTQjGyMB7o8646u6f",
  "base_amount_usd": "10",
  "expected_amount": "10.0041",
  "status": "pending",
  "confirmations": 0,
  "required_confirmations": 20,
  "created_at": "2026-09-24T12:54:44Z",
  "expires_at": "2026-09-24T13:14:44Z"
}
```

The customer must send **exactly** `expected_amount`, not
`base_amount_usd` — that's the whole matching mechanism. `address` is
the same fixed address every order on that chain gets; `qr_string` is
currently identical to `address` (a wallet scanning it just sees a plain
address, no embedded amount — add a chain-specific URI scheme here if
you want wallets to prefill the amount).

Errors: `400` for a bad/missing field or amount out of
`[0.01, 1000000]`, `400` if `chain` isn't a currently-enabled chain,
`401` for a missing/wrong `X-API-Key`, `429` if the caller's IP has
exceeded `ORDERS_RATE_LIMIT_PER_MINUTE`.

### `GET /v1/orders/{order_id}`

Public — no auth. Meant to be polled directly by a checkout frontend.
An order ID is an unguessable random UUID, not a sequential one, so this
is safe to expose. Sends `Access-Control-Allow-Origin: *` since a
frontend polling it is typically on a different origin than this
service.

Same shape as the create response, plus (once relevant):

```json
{
  "...": "...",
  "status": "underpaid",
  "tx_hash": "a1b2c3...",
  "received_amount": "6.0025",
  "confirmed_at": "2026-09-24T13:01:02Z"
}
```

`status` is one of `pending`, `confirming`, `confirmed`, `underpaid`,
`expired`. `tx_hash`/`received_amount`/`confirmed_at` are only present
once they apply.

### `GET /v1/chains`

Public. Lists every currently-enabled chain with its fixed address, so
a frontend can build a "choose a network" step without hardcoding
addresses:

```json
[
  { "chain": "tron", "address": "TRp5...", "token": "USDT", "decimals": 6, "required_confirmations": 20 },
  { "chain": "polygon", "address": "0xA742...", "token": "USDT", "decimals": 6, "required_confirmations": 128 }
]
```

### `GET /healthz`

Plain liveness check, `{"status":"ok"}`. No auth, not rate-limited.

## Configuration reference

See `.env.example` for the full list with defaults. The load-bearing
ones:

| Var | Purpose |
|---|---|
| `POSTGRES_DSN`, `REDIS_ADDR` | required; Postgres is the source of truth, Redis is rate-limit state only |
| `API_KEY` | required; the single shared secret for `POST /v1/orders` |
| `{TRON,TON,POLYGON,BSC}_ENABLED` / `_ADDRESS` | which chains are live and their fixed receiving address |
| `AMOUNT_TOLERANCE_USD` | exact-match slack, default `0.02` |
| `UNDERPAYMENT_MAX_GAP_PERCENT` | how short a payment can fall and still auto-attribute as underpaid, default `50` |
| `EXPECTED_AMOUNT_OFFSET_MIN`/`_MAX` | the random-offset range in ten-thousandths of a dollar, default `1`..`99` (i.e. `$0.0001`-`$0.0099`) |
| `ORDER_EXPIRY` | how long a `pending` order has before it expires, default `20m` |
| `{TRON,TON,POLYGON,BSC}_REQUIRED_CONFIRMATIONS`, `_POLL_INTERVAL` | per-chain finality assumption and poll cadence |
| `*_FALLBACK_*_URLS` | comma-separated fallback RPC/API endpoints per chain |

## Manual review queue

The `review_items` table is the operational surface for everything this
service couldn't (or only partially could) attribute automatically:

```sql
SELECT chain, tx_hash, amount, reason, related_order_id, detected_at
FROM review_items
WHERE status = 'open'
ORDER BY detected_at DESC;
```

- `reason = 'unmatched'`: a transfer that didn't match any pending
  order's amount within tolerance or the underpayment gap.
  `related_order_id` is `NULL`.
- `reason = 'underpaid'`: a transfer attributed to a specific order (via
  `related_order_id`) for less than its `expected_amount`.

There's no built-in resolution UI — mark a row `status = 'resolved'`
(with a note in `notes`) once you've dealt with it, e.g.:

```sql
UPDATE review_items SET status = 'resolved', resolved_at = now(),
  notes = 'refunded, wrong network' WHERE id = '...';
```

`audit_log` has a complete, append-only trail of every transfer any
watcher ever reported (`event_type = 'transfer_observed'`) plus every
state transition, for reconciliation beyond just the review queue.

## Adding a new chain

Implement `chains.ChainWatcher` (`internal/chains/watcher.go`) in a new
`internal/chains/<name>` package — `Chain()`, `RequiredConfirmations()`,
`SeedPending()`, `Run()` — reporting transfers through the
`chains.TransferSink` passed to its constructor. Register it in
`cmd/frenixpay/main.go` next to the other four. Nothing in
`internal/orders` or `internal/api` needs to change; the EVM watcher
already demonstrates chain-parameterization (Polygon and BSC share one
implementation).

## Testing

```bash
go build ./... && go vet ./...

# Amount-matching integration tests (the core logic in internal/orders)
# need a real Postgres with migrations applied:
TEST_POSTGRES_DSN="postgres://frenixpay:frenixpay@127.0.0.1:5432/frenixpay?sslmode=disable" \
  go test ./internal/orders/... -v
```

`internal/orders/service_test.go` exercises, against a live DB:
concurrent order creation never collides on `expected_amount`; an exact
match progresses `pending → confirming → confirmed` and is idempotent
under duplicate/out-of-order events; a transfer just inside the
tolerance window matches; an underpayment within the configured gap is
attributed and logged; a transfer with no plausible match is logged
unmatched exactly once even if the watcher reports it again.

Without a `TEST_POSTGRES_DSN`, these tests skip (not fail), so `go test
./...` stays green in an environment with no database.
