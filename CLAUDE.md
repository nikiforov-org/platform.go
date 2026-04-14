# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
# Build all four microservices
go build ./cmd/...

# Build individual services
go build ./cmd/gateway   # HTTP→NATS bridge
go build ./cmd/xhttp     # PostgreSQL CRUD with KV cache
go build ./cmd/xauth     # JWT authentication
go build ./cmd/xws       # WebSocket session manager

# Cross-compile for Linux production deployment
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/...
```

## Testing

No tests exist yet. When writing tests:

```bash
go test ./...
go test -v -race ./...
go test -run TestName ./internal/services/xhttp/...
```

## Demo vs. Platform Code

Services prefixed with `x` — **xhttp, xauth, xws** — are **demo examples only**. They exist to demonstrate platform structure, patterns, and capabilities. They are not production-ready and will be replaced by real business services in actual projects built on this platform. Do not spend effort on production-quality hardening of x* services.

The platform-layer code (Gateway, `internal/platform/nc`, `internal/middleware`, `utils`) is the real subject of this repository.

## Architecture

This is a **microservices platform** where all inter-service communication runs over NATS Pub/Sub. The traffic flow is:

```
HTTP Client → Gateway (:8080) → NATS (:4222) → [xhttp | xauth | xws]
```

**Gateway** (`internal/services/gateway/`) is the sole HTTP entry point. Routes:
- `GET /health` — health check (bypasses rate limiting and Origin); `200` if NATS connected, `503` otherwise. Used by Nomad.
- `/v1/{service}/{method}` — proxies HTTP → NATS Request-Reply.
- `/v1/{service}/ws` — upgrades to WebSocket, bridges to NATS Pub/Sub.

Middleware chain for `/v1/`: `Origin` → `RateLimit` → route.

Rate limiting (`internal/services/gateway/ratelimit.go`): per-IP general limit (`GATEWAY_RATE_LIMIT`, default 100 req/s) applies to all routes; optional stricter per-IP auth limit (`GATEWAY_AUTH_RATE_LIMIT`, default 5 req/s) applies to a configurable URL prefix (`GATEWAY_AUTH_RATE_PREFIX`, default empty — disabled); global WS connection counter (`GATEWAY_MAX_WS_CONNS`, default 1000).

It has no database dependency.

**xhttp** (`internal/services/xhttp/`) handles CRUD against a single PostgreSQL table (`xhttp`). Uses NATS JetStream KV (`xhttp_cache` bucket) as a caching layer. Schema migration runs inline at startup via `xhttp.Migrate(db)`.

**xauth** (`internal/services/xauth/`) implements JWT authentication (HMAC-SHA256, no external crypto libraries) with HttpOnly cookies. Stores refresh token revocation state in NATS JetStream KV (`authms_refresh_tokens`).

**xws** (`internal/services/xws/`) manages WebSocket sessions. The `Manager` holds a thread-safe registry of active sessions. Each session gets unique NATS subjects for bidirectional messaging.

### NATS Subject Routing

| Service | Subjects |
|---------|----------|
| xhttp   | `api.v1.xhttp.{create\|get\|list\|update\|delete}` |
| xauth   | `api.v1.xauth.{login\|refresh\|logout\|me}` |
| xws     | `api.v1.xws.ws.{connect\|in\|out}.{sid}` |

### Key Internal Packages

- `internal/platform/nc/` — NATS connection wrapper with JetStream KV helpers used by all services
- `internal/platform/logger/` — zerolog logger factory; call `logger.New("service-name")` in every `main.go`
- `internal/middleware/recover.go` — `Recover(log, handler)` wraps any NATS `MsgHandler`; catches panics, logs stack trace, replies 500
- `internal/middleware/xauth.go` — `RequireAuth` middleware for NATS message handlers (validates JWT from cookie header)
- `utils/reply.go` — NATS response helpers (JSON envelope format)
- `utils/env.go` — Generic `GetEnv[T]()` for environment variable parsing
- `utils/hosts.go` — CORS origin validation against `ALLOWED_HOSTS`

### Database

PostgreSQL is used only by xhttp. No ORM — raw `database/sql` with parameterized queries. The `xhttp` table has: `id` (BIGSERIAL), `name` (TEXT), `value` (TEXT), `created_at`, `updated_at`.

Connection limits in `cmd/xhttp/main.go`: max 10 open, 5 idle, 5-minute lifetime.

## Configuration

All configuration is loaded from environment variables only. Each service has a `LoadConfig()` in its package.

**Required variables by service:**

| Service | Required Env Vars |
|---------|-------------------|
| xhttp   | `DATABASE_URL` |
| xauth   | `AUTH_USERNAME`, `AUTH_PASSWORD`, `AUTH_ACCESS_SECRET`, `AUTH_REFRESH_SECRET` |
| gateway | `ALLOWED_HOSTS` (comma-separated origins, e.g. `localhost:3000,example.com`) |

All services share `NATS_HOST` (default `127.0.0.1`), `NATS_PORT` (default `4222`), `NATS_USER`, `NATS_PASSWORD`, `LOG_LEVEL` (default `info`; values: `debug`, `info`, `warn`, `error`).

## Deployment

Services are deployed via **Nomad with raw exec driver** (no Docker). The NATS cluster uses DNS-based route discovery (`deployments/nats/nats.conf`). CI/CD: `go build` → `scp` binary → `nomad job run`.
