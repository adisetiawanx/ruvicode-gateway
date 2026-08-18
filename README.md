# Ruvicode Gateway

Ruvicode is a transparent AI API gateway. One API key, one OpenAI-compatible endpoint, and access to many frontier and open AI models through a single wallet. Real per-request pricing, hard spend limits, and no credit expiry.

This repository is the Go backend for Ruvicode. It contains four binaries that share the same internal packages but run as separate processes:

- **`cmd/gateway`** is the HTTP gateway. It validates keys, enforces rate and spend limits, routes to the upstream provider, streams responses, and settles billing.
- **`cmd/pricing`** is the pricing worker. It polls the provider market feed, applies the margin spread, and refreshes the model price table and Redis cache.
- **`cmd/reconcile`** is the billing reconciliation worker. It audits per-request margins against upstream costs on an hourly schedule.
- **`cmd/monitor`** (planned, ADR-027) will poll the Base blockchain for USDC deposits, credit wallets, and manage HD wallet deposit addresses.

## Status

The request pipeline is live end to end. A request arrives, the key is validated, the per-key rate limit is enforced, the balance is pre-checked and held, the provider is resolved and called, the response is streamed back with sanitized headers, usage is parsed from the final chunk, the wallet is settled atomically, and a usage record is inserted.

Working today:

- Server setup, config, health check, graceful shutdown
- API key auth with a Redis cache and a PostgreSQL fallback
- Per-key sliding-window rate limiting
- Optimistic pre-deduction billing with atomic wallet settlement
- Streaming and non-streaming chat completions plus a public model listing endpoint
- An internal playground endpoint used by the dashboard, which resolves the caller's key, applies its limits, and bills the wallet through the normal pipeline
- A curated catalog of 33 models enforced across layers: the pricing worker only activates allowlisted slugs (sweeping strays inactive), /v1/models lists the result, and chat requests for anything outside the list are rejected before routing
- Provider abstraction with key pool rotation and identity masking
- Pass-through request forwarding: unknown client parameters ride along to the upstream verbatim, so tool calls, vision content blocks, reasoning effort, and provider-specific extras all work without gateway changes
- Upstream cost capture that tolerates every observed cost field shape (nested and top-level, object and scalar, with the settlement detail field as the source of truth)
- Automatic price sync that polls the provider market, applies the spread, and refreshes the Postgres table and Redis cache (pricing worker)
- Hourly billing reconciliation against the costs reported by the upstream (reconcile worker)
- Verified live end to end against a real provider, including streaming, billing, and masked responses

Not built yet:

- USDC deposit monitoring (ADR-027, planned)
- Metrics and alerting (ADR-036, planned)

The public surface is the OpenAI-compatible spec only. Anthropic models are served through the same `/v1/chat/completions` endpoint.

## Stack

- Go 1.26
- chi for HTTP routing
- pgx for PostgreSQL
- go-redis for Redis
- slog for structured logging
- Docker for containerization

## Repository layout

- `cmd/gateway` the HTTP gateway entrypoint
- `cmd/pricing` the pricing worker entrypoint
- `cmd/reconcile` the billing reconciliation worker entrypoint
- `cmd/monitor` the USDC deposit monitor entrypoint (planned)
- `internal/config` environment-based configuration
- `internal/server` HTTP server and route wiring
- `internal/handler` chat, models, and usage capture handlers
- `internal/middleware` auth, rate limit, and logging middleware
- `internal/store` PostgreSQL and Redis data access
- `internal/provider` provider abstraction, registry, and key pool
- `internal/billing` optimistic pre-deduction billing engine
- `internal/pricing` pricing cache and hot-path lookup
- `internal/masking` provider identity masking and error sanitization
- `internal/wallet` HD wallet and deposit address management (planned)
- `internal/monitor` USDC deposit blockchain monitor (planned)
- `migrations` SQL migrations

## Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/health` | none | Liveness and provider health |
| POST | `/v1/chat/completions` | `rvcd_` key | Chat completions, streaming and non-streaming |
| GET | `/v1/models` | none | List available models (public: coding tools discover providers by fetching this without credentials) |
| GET | `/v1` | none | API root discovery payload |
| POST | `/internal/playground/chat` | internal token | Dashboard playground, bills the caller's key |

The model list is public (no auth) so tools like OpenCode and OpenClaw can auto-discover it:

```bash
curl http://localhost:8080/v1/models
```

Authenticate chat requests with a Bearer API key:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer ***" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-opus-5","messages":[{"role":"user","content":"Hello"}]}'
```

Streaming chat adds `"stream": true` to the request body.

## Request lifecycle

1. Validate the `rvcd_` API key. Redis cache first, PostgreSQL fallback.
2. Enforce the per-key sliding-window rate limit.
3. Check spend limits and hold an estimated cost (optimistic pre-deduction).
4. Resolve the provider for the model and forward the request body verbatim (unknown parameters pass through untouched).
5. Proxy the response, strip provider headers, and inject `X-Ruvicode-*`, `X-Cost`, and user rate limit headers.
6. Parse usage from the final stream chunk, settle the actual cost atomically in PostgreSQL, and insert a usage record.

PostgreSQL is the source of truth for wallet balances. Redis is a cache only. The upstream provider is abstracted behind an interface and its identity is never exposed to clients.

## Getting started

Prerequisites are Go 1.26 and a running PostgreSQL and Redis. PostgreSQL and Redis are shared infrastructure across the Ruvicode services.

```bash
cp .env.example .env   # then fill in values
go run cmd/gateway/main.go
```

The health endpoint responds at `http://localhost:8080/health`.

```bash
curl http://localhost:8080/health
# {"provider_healthy":true,"status":"ok"}
```

## Build and test

```bash
go build ./...
go vet ./...
go test ./...
```

Docker:

```bash
docker build -t ruvicode-gateway .         # gateway
docker build -f Dockerfile.pricing -t ruvicode-pricing .   # pricing worker
```
