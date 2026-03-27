# ArkAPI

[![Live Site](https://img.shields.io/badge/live-arkapi.dev-0f766e?style=flat-square)](https://arkapi.dev)
[![License: MIT](https://img.shields.io/badge/license-MIT-2563eb?style=flat-square)](./LICENSE)
[![Go 1.25+](https://img.shields.io/badge/go-1.25+-0ea5e9?style=flat-square)](https://go.dev/)
[![Bitcoin Signet](https://img.shields.io/badge/network-Bitcoin%20Signet-f59e0b?style=flat-square)](https://en.bitcoin.it/wiki/Signet)
[![Ark Protocol](https://img.shields.io/badge/funding-Ark%20Protocol-f97316?style=flat-square)](https://ark-protocol.org/)

**Pay-per-call APIs powered by Bitcoin. No accounts. No long-lived API keys. Just sats.**

[Live Site](https://arkapi.dev) ·
[API Docs](https://arkapi.dev/docs/) ·
[OpenAPI Spec](https://arkapi.dev/openapi.json) ·
[Fund a Session](https://arkapi.dev/fund/)

ArkAPI is a Bitcoin-native API gateway that meters access to security, OSINT, AI, and utility endpoints via micropayments. Fund an anonymous session with sats, then call any endpoint until your balance runs out.

It uses [Second](https://second.tech/)'s [Bark](https://github.com/ark-bitcoin/bark) wallet and the [Ark protocol](https://ark-protocol.org/) for session funding on the Signet test network. Each session can currently be funded with either a Signet Lightning invoice or a Signet Ark address.

### Why ArkAPI?

- **No sign-up** — create a session with a single POST request, no email or OAuth required
- **No API keys to rotate** — sessions are short-lived bearer tokens funded by Bitcoin
- **Agent-friendly** — machine-readable OpenAPI spec, `llms.txt`, and `.well-known/arkapi.json` for autonomous discovery
- **Pay only for what you use** — per-call pricing from 1 to 100 sats, no subscriptions
- **Minimal data footprint** — no accounts, no email — only session state and billing records are retained
- **Self-hostable** — MIT-licensed Go binary with Docker Compose for all dependencies

---

## Prerequisites

- Go 1.25+
- Docker and Docker Compose
- MySQL or MariaDB
- Apache or another reverse proxy if you want a production-style front end
- `dig` and `whois` available on the host or container build path

---

## Quick Start

```bash
# 1. Create a Signet session
curl -X POST https://arkapi.dev/v1/sessions

# 2. Use the returned token to call APIs
TOKEN="ak_your_token_here"

curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"domain":"example.com"}' \
     https://arkapi.dev/api/dns-lookup
```

---

## Discovery & Indexing

ArkAPI publishes machine-readable metadata so AI agents and tooling can discover endpoints without custom glue code:

| URL | Purpose |
|-----|---------|
| [`openapi.json`](https://arkapi.dev/openapi.json) | Full OpenAPI 3 spec with `x-cost-sats` pricing |
| [`.well-known/arkapi.json`](https://arkapi.dev/.well-known/arkapi.json) | Lightweight service manifest |
| [`llms.txt`](https://arkapi.dev/llms.txt) | LLM-friendly plain-text summary |
| [`llms-full.txt`](https://arkapi.dev/llms-full.txt) | Extended LLM reference |
| [`sitemap.xml`](https://arkapi.dev/sitemap.xml) | XML sitemap for crawlers |

The OpenAPI spec includes bearer-token auth (`Authorization: Bearer ak_xxx`), request/response schemas, and explicit per-endpoint pricing in satoshis.

If you deploy your own instance, generate your own IndexNow verification key rather than reusing the production one.

---

## API Reference

### Public Endpoints (no auth)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check, returns `{"status":"ok"}` |
| GET | `/v1/catalog` | List all endpoints and pricing |
| POST | `/v1/sessions` | Create a new session |

### Protected Endpoints (auth required)

All require header: `Authorization: Bearer ak_xxxxx`

| Method | Path | Cost | Description |
|--------|------|------|-------------|
| GET | `/v1/balance` | free | Check session balance |
| POST | `/api/ai-chat` | 100 sats | Anonymous AI chat with ArkAPI-managed inference |
| POST | `/api/ai-translate` | 25 sats | Higher-quality AI translation with style control for more natural output |
| POST | `/api/domain-intel` | 25 sats | Aggregate DNS, WHOIS, TLS, headers, email auth, security.txt, robots.txt, improved tech fingerprints, HTTP behavior, and resolved IP intelligence |
| GET | `/api/btc-price` | 1 sat | Live Bitcoin spot price in 10 major fiat currencies, with optional currency filtering |
| POST | `/api/prediction-market-search` | 4 sats | Search open Polymarket prediction markets |
| POST | `/api/translate` | 3 sats | Self-hosted text translation with source auto-detection |
| POST | `/api/url-to-markdown` | 5 sats | Extract clean Markdown from any public URL |
| POST | `/api/axfr-check` | 12 sats | Check whether a domain allows DNS zone transfer and return exposed AXFR records when available |
| POST | `/api/cve-lookup` | 3 sats | Look up a CVE in NVD with severity, CWE, KEV, and references |
| POST | `/api/dns-lookup` | 3 sats | Full DNS records as structured JSON |
| POST | `/api/bitcoin-address` | 3 sats | Validate mainnet Bitcoin addresses and fetch on-chain balance data |
| POST | `/api/bitcoin-news` | 2 sats | Aggregated Bitcoin headlines from free RSS feeds |
| POST | `/api/cve-search` | 4 sats | Search NVD CVEs by keyword |
| POST | `/api/domain-check` | 3 sats | Check domain availability via WHOIS |
| POST | `/api/email-auth-check` | 4 sats | SPF, DKIM, and DMARC posture with A-F grade |
| POST | `/api/headers` | 3 sats | HTTP security headers audit with score |
| POST | `/api/image-generate` | 25 sats | AI image generation with short-lived download URL |
| POST | `/api/ip-lookup` | 3 sats | IP geolocation, ISP, and ASN data |
| POST | `/api/qr-generate` | 2 sats | Generate QR code PNG from text or URLs |
| POST | `/api/screenshot` | 15 sats | Server-side webpage screenshot with download URL |
| POST | `/api/ssl-check` | 5 sats | SSL certificate analysis |
| POST | `/api/weather` | 3 sats | Current weather + 7-day forecast |
| POST | `/api/whois` | 5 sats | WHOIS data parsed into clean JSON |

### Session Creation

**Signet funding mode** (current live mode):
```bash
curl -X POST -H "Content-Type: application/json" \
     -d '{"amount_sats": 500}' \
     https://arkapi.dev/v1/sessions
```
Returns both a Signet Lightning invoice and a Signet Ark address. Pay either one to activate the session:
```json
{
  "token": "ak_xxx",
  "funding": {
    "lightning_invoice": "lntbs...",
    "ark_address": "tark1...",
    "payment_hash": "abc123..."
  },
  "amount_sats": 500,
  "balance_sats": 0,
  "status": "awaiting_payment",
  "expires_in": 86400
}
```

Current live public funding page: [Fund a session](https://arkapi.dev/fund/)

This deployment is live on the **Signet test network only**. The same session object supports both funding routes, and ArkAPI activates the balance once either payment settles.

### Request/Response Examples

<details>
<summary>Domain Intel (25 sats)</summary>

```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"domain":"example.com","ai_summary":true}' \
     https://arkapi.dev/api/domain-intel
```
Returns DNS, WHOIS, TLS, headers, email auth, security.txt, robots.txt, tech fingerprints, HTTP behavior, subdomain hints, CT subdomains, network summary, findings, recommendations, and an optional AI summary.

Public guide: [Domain Intel](https://arkapi.dev/domain-intel/)
</details>

<details>
<summary>Anonymous AI Chat (100 sats)</summary>

```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"prompt":"How is Ark different from Lightning?"}' \
     https://arkapi.dev/api/ai-chat
```

Public guide: [Anonymous AI Chat](https://arkapi.dev/ai-chat/)
</details>

<details>
<summary>AI Translate (25 sats)</summary>

```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"text":"Bonjour tout le monde","target_language":"en","style":"natural"}' \
     https://arkapi.dev/api/ai-translate
```

Public guide: [AI Translate](https://arkapi.dev/ai-translate/)
</details>

<details>
<summary>BTC Price (1 sat)</summary>

```bash
curl -H "Authorization: Bearer $TOKEN" \
     https://arkapi.dev/api/btc-price

curl -H "Authorization: Bearer $TOKEN" \
     "https://arkapi.dev/api/btc-price?currency=USD"

curl -H "Authorization: Bearer $TOKEN" \
     "https://arkapi.dev/api/btc-price?currencies=USD,EUR,CAD"
```

Public guide: [BTC Price](https://arkapi.dev/btc-price/)
</details>

<details>
<summary>Prediction Market Search (4 sats)</summary>

```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"query":"bitcoin etf","limit":5}' \
     https://arkapi.dev/api/prediction-market-search
```

Public guide: [Prediction Market Search](https://arkapi.dev/prediction-market-search/)
</details>

<details>
<summary>More endpoint examples (Translate, URL to Markdown, AXFR, CVE, DNS, WHOIS, SSL, Headers, Weather, IP)</summary>

**Translate (3 sats):**
```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"text":"Hola, me llamo ArkAPI.","target_language":"en"}' \
     https://arkapi.dev/api/translate
```

**URL to Markdown (5 sats):**
```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"url":"https://example.com"}' \
     https://arkapi.dev/api/url-to-markdown
```

**AXFR Check (12 sats):**
```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"domain":"example.com"}' \
     https://arkapi.dev/api/axfr-check
```

**CVE Lookup (3 sats):**
```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"cve":"CVE-2024-3400"}' \
     https://arkapi.dev/api/cve-lookup
```

**DNS Lookup (3 sats):**
```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"domain":"example.com"}' \
     https://arkapi.dev/api/dns-lookup
```

**WHOIS (5 sats):**
```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"domain":"example.com"}' \
     https://arkapi.dev/api/whois
```

**SSL Check (5 sats):**
```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"domain":"google.com"}' \
     https://arkapi.dev/api/ssl-check
```

**HTTP Headers Audit (3 sats):**
```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"url":"https://google.com"}' \
     https://arkapi.dev/api/headers
```

**Weather (3 sats):**
```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"city":"New York"}' \
     https://arkapi.dev/api/weather
```

**IP Lookup (3 sats):**
```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"ip":"8.8.8.8"}' \
     https://arkapi.dev/api/ip-lookup
```
</details>

### Response Format

All API endpoints return a consistent response wrapper:
```json
{
  "success": true,
  "data": { ... },
  "cost_sats": 3,
  "balance_remaining": 9997,
  "response_ms": 142,
  "endpoint": "/api/dns-lookup"
}
```

On error (no charge):
```json
{
  "success": false,
  "error": "description of what went wrong",
  "cost_sats": 0,
  "endpoint": "/api/dns-lookup"
}
```

---

## Architecture

```
Internet
   |
   v
Cloudflare (SSL termination)
   |
   v
Apache (reverse proxy on host)
   |  proxies /health, /v1/*, /api/* to localhost:8080
   |  serves static landing page at /
   v
+-----------------------------------------------+
|  Docker / Host Services                        |
|                                                |
|  +--------------+    +----------------------+  |
|  |   arkapi     |--->|   bark (barkd)       |  |
|  |   Go binary  |    |   Ark wallet         |  |
|  |   :8080      |    |   :3000 (localhost)  |  |
|  |   (host net) |    +----------------------+  |
|  +------+-------+                               |
|         |            +----------------------+   |
|         +----------->| ComfyUI              |   |
|         |            | :8188 (localhost)    |   |
|         |            +----------------------+   |
|         |            +----------------------+   |
|         +----------->| LibreTranslate       |   |
|         |            | :5001 (localhost)    |   |
|         |            +----------------------+   |
|         |            +----------------------+   |
|         +----------->| Screenshotter        |   |
|         |            | :9010 (localhost)    |   |
|         |            +----------------------+   |
|         |                                       |
|         +-----------> MySQL :3306 (host)        |
+---------|---------------------------------------+
          |
          +----> External upstreams
                 - Cloudflare AI
                 - Open-Meteo
                 - NVD API
                 - Polymarket Gamma API
                 - Public DNS / WHOIS / RDAP services
```

### Components

- **Apache** — Example reverse proxy on the host. Routes `/health`, `/v1/*`, and `/api/*` to the Go backend and can serve the static site at `/`.
- **Cloudflare** — Optional DNS, TLS, and CDN layer in front of the web tier.
- **arkapi container** — Go binary running with `network_mode: host`. Serves the API on `127.0.0.1:8080`, handles sessions, auth, rate limiting, billing, and calls both local helper services and external upstream APIs. Installs `dig`, `whois`, and `curl` for command-based handlers.
- **bark container** — Second's `barkd` daemon on Bitcoin Signet. Exposes a REST API on `127.0.0.1:3000` and handles session funding detection for both Lightning invoices and Ark-address funding. Wallet data is persisted in the `bark-data` Docker volume.
- **ComfyUI** — Local image generation backend on `127.0.0.1:8188` used by `/api/image-generate`.
- **translate container** — Self-hosted LibreTranslate service on `127.0.0.1:5001`.
- **screenshotter container** — Playwright-based screenshot service on `127.0.0.1:9010`.
- **MySQL** — Host database on `127.0.0.1:3306` for sessions, balances, and call logs. The `arkapi` MySQL user is scoped to the `arkapi` database.
- **DB-IP Lite MMDB files** — Self-hosted IP geolocation databases read from disk by `/api/ip-lookup`. Not an external service.
- **External upstreams** — ArkAPI also calls external services where local infrastructure is not the source of truth:
  - **Cloudflare AI** for `/api/ai-chat` and `/api/ai-translate`
  - **Open-Meteo** for `/api/weather`
  - **NVD API** for `/api/cve-search` and `/api/cve-lookup`
  - **Polymarket Gamma API** for `/api/prediction-market-search`
  - **Public WHOIS, RDAP, and DNS infrastructure** for domain and registration intelligence

### AI Chat Security Notes

- `/api/ai-chat` is limited to `5/day/token`.
- Exact repeat requests may be served from the ArkAPI cache, but successful calls are still billed.
- Caller-supplied `system_prompt` is rejected.
- Caller-supplied chat history may contain only `user` and `assistant` roles; `system` role input is rejected.
- The built-in system instructions explicitly refuse disclosure of hidden prompts or internal configuration.

### Networking

- arkapi uses `network_mode: host` so it can reach host-local MySQL and localhost-bound helper services.
- arkapi itself is still intended to be reached through Apache and, optionally, Cloudflare.
- bark exposes port 3000 to `127.0.0.1` only — not accessible from the internet.
- ComfyUI exposes port 8188 to `127.0.0.1` only — not accessible from the internet.
- LibreTranslate exposes port 5001 to `127.0.0.1` only — not accessible from the internet.
- Screenshotter exposes port 9010 to `127.0.0.1` only — not accessible from the internet.
- In the reference deployment, only the web tier is internet-facing on ports `80` and `443`. Bark, ComfyUI, translation, screenshot, and database services stay bound to localhost.
- If you want optional traffic reporting, set `ARKAPI_ADMIN_TRAFFIC_LOG_PATH` to a readable web access log path.

---

## Server Operations

### File Locations

The paths below use the reference deployment layout. Adjust to match your setup.

| Path | Purpose |
|------|---------|
| `./` | Project root — source code and compose files |
| `./.env` | Environment variables (chmod 600, not in git) |
| `./docker-compose.yml` | Main services (arkapi + bark + translate + screenshotter) |
| `./translate/Dockerfile` | Dedicated self-hosted LibreTranslate container |
| `./docker-compose.consumer.yml` | Test consumer wallet |
| `site/` | Static site files (served by your reverse proxy) |

### Docker Commands

```bash
# View running containers
sudo docker compose ps

# View logs
sudo docker logs arkapi
sudo docker logs bark
sudo docker logs arkapi-translate
sudo docker logs arkapi-screenshotter

# Restart everything
sudo docker compose restart

# Rebuild and restart (after code changes)
sudo docker compose up -d --build

# Stop everything
sudo docker compose down
```

### Bark Wallet Management

```bash
# Check wallet balance (via REST API since barkd holds the lock)
curl -s http://127.0.0.1:3000/api/v1/wallet/balance

# Get wallet address
curl -s -X POST http://127.0.0.1:3000/api/v1/wallet/addresses/next

# List pending Lightning invoices
curl -s http://127.0.0.1:3000/api/v1/lightning/receives

# Fund from signet faucet
# https://signet.2nd.dev
```

### Run Tests

```bash
# Full API test suite (test mode)
bash scripts/test.sh

# Bark payment flow test (requires funded consumer wallet)
bash scripts/test-bark.sh
```

### Switch Payment Modes

Edit `.env`:

```bash
# Test mode — instant free balance, no bark needed
ARKAPI_PAYMENT_MODE=test

# Bark mode — requires Lightning payment to activate sessions
ARKAPI_PAYMENT_MODE=bark
```

Then rebuild: `sudo docker compose up -d --build`

---

## Configuration

All configuration is via environment variables. Copy `.env.example` to `.env` and edit:

| Variable | Default | Description |
|----------|---------|-------------|
| `ARKAPI_PORT` | `8080` | HTTP server port |
| `ARKAPI_BIND_HOST` | `127.0.0.1` | HTTP server bind address |
| `ARKAPI_DB_USER` | `arkapi` | MySQL username |
| `ARKAPI_DB_PASS` | — | MySQL password |
| `ARKAPI_DB_HOST` | `127.0.0.1` | MySQL host |
| `ARKAPI_DB_PORT` | `3306` | MySQL port |
| `ARKAPI_DB_NAME` | `arkapi` | MySQL database name |
| `ARKAPI_PAYMENT_MODE` | `test` | `test` or `bark` |
| `ARKAPI_BARK_URL` | `http://127.0.0.1:3000` | barkd REST API URL |
| `ARKAPI_BARK_TOKEN` | — | barkd auth token (if required) |
| `ARKAPI_DEFAULT_BALANCE_SATS` | `10000` | Starting balance in test mode |
| `ARKAPI_SESSION_TTL_HOURS` | `24` | Session expiry (hours) |
| `ARKAPI_SESSION_CREATE_LIMIT` | `10` | Max session creates per window |
| `ARKAPI_SESSION_CREATE_WINDOW_SECONDS` | `3600` | Rate limit window for session creation |
| `ARKAPI_API_RATE_LIMIT` | `60` | Max API calls per window per IP |
| `ARKAPI_API_RATE_WINDOW_SECONDS` | `60` | Rate limit window for API calls |

---

## Database Schema

Two tables in the `arkapi` MySQL database:

**sessions** — funded API sessions
```sql
token              VARCHAR(64)  PRIMARY KEY
balance_sats       BIGINT       NOT NULL DEFAULT 0
status             ENUM('awaiting_payment','active','expired')
created_at         TIMESTAMP
last_used_at       TIMESTAMP    NULL
expires_at         TIMESTAMP    NULL
payment_hash       VARCHAR(255) NULL
lightning_invoice  TEXT         NULL
```

**call_log** — every API call recorded
```sql
id                 BIGINT       AUTO_INCREMENT PRIMARY KEY
session_token      VARCHAR(64)  NOT NULL
endpoint           VARCHAR(100) NOT NULL
cost_sats          INT          NOT NULL
response_ms        INT          NOT NULL DEFAULT 0
status_code        SMALLINT     NOT NULL DEFAULT 200
created_at         TIMESTAMP
```

---

## Security

- **Local-only service bindings** — the reference deployment exposes only the web tier on `80/443`; backend services stay on localhost
- **SSRF protection** — `/api/headers` rejects loopback, private, link-local, and cloud metadata IPs
- **Rate limiting** — per-IP/path limits on session creation and API calls
- **Session expiry** — enforced in auth middleware, refreshed on each use
- **Request size limits** — 1MB body cap, no trailing JSON accepted
- **Non-root container** — arkapi runs as `uid=999` inside the container
- **MySQL isolation** — `arkapi` user has access only to the `arkapi` database
- **`.env` permissions** — `chmod 600`, outside the web root
- **Bark localhost-only** — barkd REST API bound to `127.0.0.1:3000`, not internet-accessible
- **SQL injection safe** — all queries use parameterized statements
- **Command injection safe** — domain/IP inputs validated before shell execution

### Wallet Seed Phrase Backup

Signet seed phrases are stored locally in `WALLETS.md` (chmod 600, gitignored) and inside the Docker volumes.
The public repo can carry `WALLETS.example.md` as a safe template, but never the real seed file.

**Before mainnet migration:**
1. Generate **new** wallets — do not reuse signet seeds
2. Back up the merchant seed phrase to a secure location **outside Docker** (encrypted backup, hardware wallet, or password manager)
3. Never commit seed phrases to git
4. The consumer test wallet is disposable and not needed on mainnet

To view seed phrases:
```bash
# Merchant
sudo docker exec bark cat /root/.bark/mnemonic

# Consumer (test only)
sudo docker exec bark-consumer cat /root/.bark/mnemonic
```

### Pre-launch Checklist

- [ ] Set `ARKAPI_DEFAULT_BALANCE_SATS=0` or `ARKAPI_PAYMENT_MODE=bark` before public launch
- [ ] Consider restricting CORS origins from `*` to specific domains
- [ ] Fund the merchant Bark wallet from a Signet faucet for test deployments
- [ ] Test at least one full session activation flow end-to-end using Lightning, Ark funding, or both
- [ ] Back up merchant wallet seed phrase securely before mainnet
- [ ] Confirm `.env`, `WALLETS.md`, and `CLOUDFLARE.md` remain untracked before publishing

### Publishing Hygiene

- Keep live secrets only in local private files such as `.env`, `WALLETS.md`, and `CLOUDFLARE.md`
- Publish sanitized templates such as `.env.example`, `WALLETS.example.md`, and `CLOUDFLARE.example.md` instead of the real files
- Keep `go.sum` in the repo so builds stay reproducible

---

## Funding Flow (Bark / Ark)

```
Consumer                        ArkAPI                          barkd
   |                              |                               |
   |  POST /v1/sessions           |                               |
   |  {"amount_sats": 5000}       |                               |
   |----------------------------->|                               |
   |                              |  create funding options       |
   |                              |------------------------------>|
   |                              |<------------------------------|
   |  {"token":"ak_...",          |                               |
   |   "funding": {               |                               |
   |     "lightning_invoice":     |                               |
   |     "ark_address":           |                               |
   |   },                         |                               |
   |   "status":"awaiting_payment"}                              |
   |<-----------------------------|                               |
   |                              |                               |
   |  Option A: pay Lightning invoice                             |
   |  Option B: send sats to Ark address                          |
   |----------------------------->|------------------------------>|
   |                              |                               |
   |                              |  poll / wallet state          |
   |                              |------------------------------>|
   |                              |<------------------------------|
   |                              |                               |
   |                              |  session activated            |
   |                              |  balance_sats = 5000          |
   |                              |                               |
   |  POST /api/domain-intel      |                               |
   |  Authorization: Bearer ak_...|                               |
   |----------------------------->|                               |
   |  {"success": true,           |                               |
   |   "cost_sats": 25,           |                               |
   |   "balance_remaining": 4975} |                               |
   |<-----------------------------|                               |
```

---

## Project Structure

```
arkapi/
├── cmd/arkapi/main.go              # Entry point, routing, server startup
├── internal/
│   ├── bark/
│   │   ├── client.go               # barkd REST API client
│   │   └── poller.go               # Background payment detection
│   ├── config/config.go            # Environment-based configuration
│   ├── database/database.go        # MySQL operations (sessions, billing)
│   ├── handlers/
│   │   ├── handlers.go             # Shared handler logic, response formatting
│   │   ├── session.go              # Session creation (test + bark modes)
│   │   ├── admin.go                # Admin/traffic endpoints
│   │   ├── ai_chat.go              # Anonymous AI chat (Cloudflare AI)
│   │   ├── ai_translate.go         # AI-powered translation
│   │   ├── axfr_check.go           # DNS zone transfer check
│   │   ├── bitcoin_address.go      # Bitcoin address validation
│   │   ├── bitcoin_news.go         # Bitcoin RSS news aggregation
│   │   ├── bitcoin_price.go        # Live BTC spot price
│   │   ├── cve_lookup.go           # CVE lookup (NVD)
│   │   ├── cve_search.go           # CVE keyword search (NVD)
│   │   ├── dns_lookup.go           # DNS record lookup
│   │   ├── domain_check.go         # Domain availability check
│   │   ├── domain_intel.go         # Aggregate domain intelligence
│   │   ├── email_auth_check.go     # SPF/DKIM/DMARC audit
│   │   ├── headers.go              # HTTP security headers audit (SSRF-safe)
│   │   ├── image_generate.go       # AI image generation (ComfyUI)
│   │   ├── ip_lookup.go            # IP geolocation (DB-IP Lite)
│   │   ├── markdown.go             # URL to Markdown extraction
│   │   ├── prediction_market_search.go  # Polymarket search
│   │   ├── qr_generate.go          # QR code generation
│   │   ├── screenshot.go           # Webpage screenshot
│   │   ├── ssl_check.go            # SSL certificate analysis
│   │   ├── translate.go            # Self-hosted translation (LibreTranslate)
│   │   ├── weather.go              # Weather forecast (Open-Meteo)
│   │   └── whois.go                # WHOIS lookup
│   └── middleware/
│       ├── auth.go                 # Bearer token auth + session validation
│       └── rate_limit.go           # Per-IP/path rate limiting
├── scripts/
│   ├── bark-init.sh                # Bark wallet init + daemon startup
│   ├── test.sh                     # API test suite (test mode)
│   └── test-bark.sh                # Lightning payment flow test
├── sql/
│   ├── schema.sql                  # Initial database schema
│   └── 002_bark_columns.sql        # Bark payment columns migration
├── Dockerfile                      # ArkAPI Go container (multi-stage)
├── Dockerfile.bark                 # Bark wallet daemon container
├── docker-compose.yml              # Production services
├── docker-compose.consumer.yml     # Test consumer wallet
├── .env.example                    # Configuration template (copy to .env)
├── .dockerignore
├── .gitignore
└── go.mod
```

---

## Technology

- **Go 1.25** — core API server and request orchestration
- **MySQL or MariaDB** — sessions, balances, and billing logs
- **Docker Compose** — service orchestration for ArkAPI and local helper services
- **Apache or another reverse proxy** — front-end web tier in the reference deployment
- **Cloudflare** — DNS, TLS/CDN, and AI inference for chat and AI translation
- **Second Bark v0.1.0-beta.8** — Ark wallet daemon for session funding detection
- **Bitcoin Signet** — current live funding network for testing
- **ComfyUI** — local image generation backend for `/api/image-generate`
- **LibreTranslate** — local translation backend for `/api/translate`
- **Playwright** — local screenshot rendering backend for `/api/screenshot`
- **Open-Meteo** — upstream weather data for `/api/weather`
- **DB-IP Lite** — self-hosted MMDB files for IP geolocation in `/api/ip-lookup`
- **NVD API** — upstream CVE data for search and lookup
- **Polymarket Gamma API** — upstream market discovery data
- **RDAP / WHOIS / DNS tooling** — registration and domain intelligence inputs
- **ARM64-friendly deployment** — the sample Bark image currently targets ARM64

---

## Contributing

Contributions are welcome! Please open an issue first to discuss what you'd like to change.

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-change`)
3. Commit your changes
4. Open a pull request against `main`

---

## License

[MIT](./LICENSE) &copy; [PiHiker](https://github.com/PiHiker)
