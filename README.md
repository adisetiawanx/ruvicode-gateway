# Ruvicode Gateway

Ruvicode is a transparent AI API gateway. One API key, one OpenAI-compatible endpoint, and access to many frontier and open AI models through a single wallet. Real per-request pricing, hard spend limits, and no credit expiry.

This repository is the Go gateway. It is the request-processing core of Ruvicode. For every API request it validates the key, enforces per-key rate and spend limits, routes to an upstream provider, streams the model response, and records usage and cost.

## Overview

The gateway sits between API clients and upstream model providers. It exposes an OpenAI-compatible chat completions endpoint and an Anthropic-compatible messages endpoint. The upstream provider is kept abstracted through a provider interface and is never exposed to clients.

## Status

Project skeleton (ADR-016). The server builds, boots, exposes a health check, connects to PostgreSQL and Redis, and shuts down gracefully. The core request pipeline (auth, rate limiting, billing, streaming, provider abstraction) is specified in the architecture decision records and is being implemented incrementally.

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
- `internal/handler` HTTP handlers
- `internal/store` PostgreSQL and Redis clients
- `internal/provider` provider abstraction (planned)
- `internal/billing` billing engine (planned)
- `internal/masking` provider identity masking (planned)
- `migrations` SQL migrations

## Getting started

Prerequisites are Go 1.26 and a running PostgreSQL and Redis. PostgreSQL and Redis are shared infrastructure across the Ruvicode services.

```bash
cp .env.example .env   # then fill in values
go run cmd/gateway/main.go
```

The health endpoint responds at `http://localhost:8080/health`.

```bash
curl http://localhost:8080/health
# {"status":"ok"}
```

## Build and test

```bash
go build ./...
go vet ./...
```

Docker:

```bash
docker build -t ruvicode-gateway .
docker run --env-file .env -p 8080:8080 ruvicode-gateway
```
