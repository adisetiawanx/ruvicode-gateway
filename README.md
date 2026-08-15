# Ruvicode Gateway

Ruvicode is a transparent AI API gateway. One API key, one OpenAI-compatible endpoint, and access to many frontier and open AI models through a single wallet. Real per-request pricing, hard spend limits, and no credit expiry.

This repository is the Go gateway. It is the request-processing core of Ruvicode. For every API request it validates the key, enforces per-key rate and spend limits, routes to an upstream provider, streams the model response, and records usage and cost.

## Status

The request pipeline is live end to end. A request arrives, the key is validated, the per-key rate limit is enforced, the balance is pre-checked and held, the provider is resolved and called, the response is streamed back with sanitized headers, usage is parsed from the final chunk, the wallet is settled atomically, and a usage record is inserted.

Working today:

- Server setup, config, health check, graceful shutdown
- API key auth with a Redis cache and a PostgreSQL fallback
- Per-key sliding-window rate limiting
- Optimistic pre-deduction billing with atomic wallet settlement
- Streaming and non-streaming chat completions plus a model listing endpoint
- An internal playground endpoint used by the dashboard, which resolves the caller's key, applies its limits, and bills the wallet through the normal pipeline
- Provider abstraction with key pool rotation and identity masking
- Upstream cost capture that tolerates every observed cost field shape (nested and top-level, object and scalar, with the settlement detail field as the source of truth)
- Automatic price sync that polls the provider market, applies the spread, and refreshes the Postgres table and Redis cache (pricing worker)
- Hourly billing reconciliation against the costs reported by the upstream (reconcile worker)
- Verified live end to end against a real provider, including streaming, billing, and masked responses

Not built yet:

- USDC deposit monitoring, and metrics and alerting.

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
- `internal/config` environment-based configuration
- `internal/server` HTTP server and route wiring
- `internal/handler` chat, models, and usage capture handlers
- `internal/middleware` auth, rate limit, and logging middleware
- `internal/store` PostgreSQL and Redis data access
- `internal/provider` provider abstraction, registry, and key pool
- `internal/billing` optimistic pre-deduction billing engine
- `internal/pricing` pricing cache and hot-path lookup
- `internal/masking` provider identity masking and error sanitization
- `migrations` SQL migrations

## Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/health` | none | Liveness and provider health |
| POST | `/v1/chat/completions` | `rvcd_` key | Chat completions, streaming and non-streaming |
| GET | `/v1/models` | `rvcd_` key | List available models |
| POST | `/internal/playground/chat` | internal token | Dashboard playground, bills the caller's key |

Authenticate with a Bearer API key:

```bash
curl http://localhost:8080/v1/models \
  -H "Authorization: Bearer rvcd_..."
```

Non-streaming chat:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer rvcd_..." \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-opus-4.7","messages":[{"role":"user","content":"Hello"}]}'
```

Streaming chat adds `"stream": true` to the request body.

## Request lifecycle

1. Validate the `rvcd_` API key. Redis cache first, PostgreSQL fallback.
2. Enforce the per-key sliding-window rate limit.
3. Check spend limits and hold an estimated cost (optimistic pre-deduction).
4. Resolve the provider for the model and forward the request.
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
docker build -t ruvicode-gateway .
docker run --env-file .env -p 8080:8080 ruvicode-gateway
```

The pricing worker builds from the same source with its own Dockerfile:

```bash
docker build -f Dockerfile.pricing -t ruvicode-pricing .
```
