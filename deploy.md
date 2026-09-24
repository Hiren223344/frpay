# Deploying Frenix Pay on your own VPS

Frenix Pay is a single self-contained Go binary plus Postgres and Redis.
There's no third-party payment processor and no Cloudflare dependency —
everything here runs on infrastructure you control. This assumes a
fresh Debian/Ubuntu VPS; adjust package manager commands for other
distros.

## 1. Prerequisites

- A VPS with outbound HTTPS access to whichever chain RPC/API endpoints
  you enable (TronGrid, toncenter.com, and your Polygon/BSC RPC
  providers).
- Go 1.24+ if you're building on the VPS itself (`go build` needs no
  other toolchain — this is a static Go binary with no CGo).
- Postgres 14+ and Redis 6+.
- A domain name if you want TLS (recommended — `API_KEY` and order data
  go over this).

## 2. Install Postgres and Redis

```bash
sudo apt update
sudo apt install -y postgresql redis-server

sudo systemctl enable --now postgresql redis-server
```

Create the database and a dedicated role:

```bash
sudo -u postgres psql -c "CREATE USER frenixpay WITH PASSWORD '<a strong random password>';"
sudo -u postgres psql -c "CREATE DATABASE frenixpay OWNER frenixpay;"
```

Redis needs no setup beyond running — it's used only as a rate-limit
cache, never as durable state.

## 3. Build the binary

```bash
git clone <your fork of this repo> /opt/frenixpay/src
cd /opt/frenixpay/src
go build -o /opt/frenixpay/frenixpay ./cmd/frenixpay
```

`/opt/frenixpay/src/migrations` needs to stay next to wherever you point
`MIGRATIONS_DIR` (see below) — either run the binary from the repo
checkout, or copy `migrations/` alongside the binary.

To update later: `git pull`, rebuild, restart the service (§6) —
migrations apply automatically on every startup, there's no separate
migration step.

## 4. Create a dedicated service user

Never run this as root.

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin frenixpay
sudo mkdir -p /etc/frenixpay
sudo chown root:frenixpay /etc/frenixpay
```

## 5. Configure the environment file

```bash
sudo cp /opt/frenixpay/src/.env.example /etc/frenixpay/frenixpay.env
sudo chmod 640 /etc/frenixpay/frenixpay.env
sudo chown root:frenixpay /etc/frenixpay/frenixpay.env
```

Edit `/etc/frenixpay/frenixpay.env`. At minimum:

```bash
POSTGRES_DSN=postgres://frenixpay:<password>@127.0.0.1:5432/frenixpay?sslmode=disable
REDIS_ADDR=127.0.0.1:6379

# Generate with: openssl rand -hex 32
API_KEY=<random secret — this is the only credential guarding order creation>

MIGRATIONS_DIR=/opt/frenixpay/src/migrations

TRON_ENABLED=true
TRON_ADDRESS=<your TRON receiving address>

TON_ENABLED=true
TON_ADDRESS=<your TON receiving address>

POLYGON_ENABLED=true
POLYGON_ADDRESS=<your 0x receiving address>

BSC_ENABLED=true
BSC_ADDRESS=<your 0x receiving address — can be the same as Polygon's>
```

Every address here is **watch-only** — this service only ever reads
public on-chain data against them. It never needs, and never asks for,
a private key. Double-check every address before going live: a typo
here means real customer payments go somewhere this service isn't
watching.

Leave a chain's `*_ENABLED=false` if you don't want it live yet; `GET
/v1/chains` only lists enabled chains, and `POST /v1/orders` rejects a
`chain` that isn't enabled.

If you're relying on the free public TronGrid/toncenter/RPC endpoints,
consider getting a free TronGrid API key (`TRON_API_KEY`) and a
toncenter API key (`TON_API_KEY`) to raise your rate limits, and set
`*_FALLBACK_*_URLS` to a comma-separated backup list per chain so a
single provider outage doesn't stop detection.

## 6. systemd unit

`/etc/systemd/system/frenixpay.service`:

```ini
[Unit]
Description=Frenix Pay
After=network-online.target postgresql.service redis-server.service
Wants=network-online.target

[Service]
Type=simple
User=frenixpay
Group=frenixpay
WorkingDirectory=/opt/frenixpay/src
EnvironmentFile=/etc/frenixpay/frenixpay.env
ExecStart=/opt/frenixpay/frenixpay
Restart=on-failure
RestartSec=5

# Hardening: the service only ever talks to Postgres/Redis and outbound
# HTTPS, so it needs no filesystem write access and no capabilities.
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
CapabilityBoundingSet=

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now frenixpay
sudo systemctl status frenixpay
```

Logs are structured JSON to stdout, captured by journald:

```bash
journalctl -u frenixpay -f
```

Verify it's up:

```bash
curl -sS http://127.0.0.1:8080/healthz
curl -sS http://127.0.0.1:8080/v1/chains
```

## 7. Put it behind TLS

Don't expose port 8080 directly. Terminate TLS with a reverse proxy and
only open 443 (and 22 for SSH) in your firewall.

**Caddy** (simplest — automatic Let's Encrypt certs):

```
pay.yourdomain.com {
    reverse_proxy 127.0.0.1:8080
}
```

**nginx**, if you already run it:

```nginx
server {
    listen 443 ssl http2;
    server_name pay.yourdomain.com;

    ssl_certificate     /etc/letsencrypt/live/pay.yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/pay.yourdomain.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

`GET /v1/orders/{id}` and `GET /v1/chains` send
`Access-Control-Allow-Origin: *` themselves — don't add another CORS
layer in the proxy or you'll end up with duplicate headers.

## 8. Firewall

```bash
sudo ufw default deny incoming
sudo ufw allow 22/tcp
sudo ufw allow 443/tcp
sudo ufw enable
```

Postgres and Redis should only ever be bound to `127.0.0.1` (the
Debian/Ubuntu package defaults already do this) — never exposed to the
public interface.

## 9. Watching the manual-review queue

`review_items` is where every transfer this service couldn't fully
attribute ends up (see [docs.md](docs.md#manual-review-queue)) — nothing
is ever silently dropped, but nothing pages you either unless you set
that up. A simple cron-based check:

```bash
# /etc/cron.d/frenixpay-review-alert — every 15 minutes
*/15 * * * * frenixpay psql "$POSTGRES_DSN" -tAc \
  "SELECT count(*) FROM review_items WHERE status='open'" | \
  awk '$1>0 {print "frenixpay: " $1 " open review items"}' \
  # pipe this into whatever alerting you already use (mail, a webhook, etc.)
```

Adapt to however you already do alerting — the point is: don't let
`review_items` grow unattended, since every row is real money.

## 10. Backups

```bash
# /etc/cron.d/frenixpay-backup — nightly at 03:00
0 3 * * * postgres pg_dump frenixpay | gzip > /var/backups/frenixpay-$(date +\%F).sql.gz
```

`orders` contains `user_id` values from your own system — treat backups
with the same care as any other customer data. Nothing in this
database is a private key or a secret that could move funds (the
service never holds one), but `API_KEY` lives in
`/etc/frenixpay/frenixpay.env`, not the database, so back that file up
separately (and keep it out of version control).

## 11. Rolling back

```bash
cd /opt/frenixpay/src
git checkout <previous tag/commit>
go build -o /opt/frenixpay/frenixpay ./cmd/frenixpay
sudo systemctl restart frenixpay
```

Migrations only ever run forward automatically. If a specific migration
needs reverting, use the `golang-migrate` CLI directly against
`POSTGRES_DSN` with the `.down.sql` file for that version — check what
you're reverting first, since `orders`/`review_items`/`audit_log` are
your transaction history.
