# Ruvicode Gateway

Ruvicode is a transparent AI API gateway. One API key, one OpenAI-compatible endpoint, and access to many frontier and open AI models through a single wallet. Real per-request pricing, hard spend limits, and no credit expiry.

This repository is the Go gateway. It is the request-processing core of Ruvicode. For every API request it validates the key, enforces per-key rate and spend limits, routes to an upstream provider, streams the model response, and records usage and cost.

## Status

The gateway core is implemented. The server builds, boots, connects to PostgreSQL and Redis, and shuts down gracefully. The request pipeline is live: API key auth, per-key rate limiting, optimistic billing with wallet deduction, streaming and non-streaming chat completions, a model listing endpoint, and provider identity masking.

Implemented so far:

- ADR-016 project setup, config, stores, health check
- ADR-017 provider abstraction, registry, key pool, concrete provider client
- ADR-018 gateway core, auth, rate limiting, chat proxy, billing integration

Remaining work is tracked in the architecture decision records (ADR-019 billing reconciliation, ADR-020 pricing worker, ADR-021 API key management, ADR-022 masking hardening, ADR-023 deployment, ADR-024 USDC monitoring, ADR-025 observability).

## Stack

- Go 1.26
- chi for HTTP routing
- pgx for PostgreSQL
- go-redis for Redis
- slog for structured logging
- Docker for containerization

## Repository layout

- `cmd/gateway` the HTTP gateway entrypoint
- `cmd/pricing` the pricing worker entrypoint (placeholder)
- `cmd/reconcile` the billing reconciliation worker entrypoint (placeholder)
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
| POST | `/anthropic/v1/messages` | `rvcd_` key | Registered, not yet implemented |
| GET | `/anthropic/v1/models` | `rvcd_` key | Registered, not yet implemented |

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
