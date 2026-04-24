# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Audit status

Единый рабочий файл аудита платформы: [`docs/audit/STATUS.md`](docs/audit/STATUS.md). Читать в начале любой сессии, связанной с правкой/разбором кода. Источник правды о состоянии открытых (`- [ ]`) и закрытых (`- [x]`) находок. Приоритет: Critical > High > Medium > Low. ID находок стабильные (`P-C1`, `I-H2`, `D-M3`, `G4` — P=platform, I=infra/CI, D=demo, G=global). После фикса — поменять чекбокс на `[x]` с пометкой даты и краткого описания, в том же файле.

## Local Development

Dev-окружение запускается скриптом `deployments/envs/dev/start.sh`:

```bash
# 1 нода — NATS single-node + Nomad -dev (быстрый старт)
./deployments/envs/dev/start.sh

# N нод — NATS cluster + Nomad cluster из N агентов
./deployments/envs/dev/start.sh 3

# Остановить всё
./deployments/envs/dev/start.sh stop
```

Скрипт поднимает Docker Compose (NATS + PostgreSQL), собирает бинарники в `./bin/`, запускает Nomad и деплоит джобы. Подробнее: `deployments/envs/dev/dev.md`.

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
- `GET /health` — health check (bypasses rate limiting and Origin); `200` if NATS connected, `503` otherwise.
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

Gateway ретраит NATS-запросы при `ErrNoResponders` (нет живых подписчиков) в пределах `GATEWAY_NATS_REQUEST_TIMEOUT`, с паузой `GATEWAY_NATS_RETRY_DELAY` (default `100ms`) между попытками. Это закрывает короткое окно при перетасовке копий сервиса на другой ноде; прочие ошибки возвращаются клиенту без повторов.

`NATS_KV_REPLICAS` не задаётся — платформа определяет число реплик автоматически по размеру кластера (`len(conn.Servers())`) после подключения к NATS.

**Important dev-only overrides:**
- `COOKIE_SECURE=false` — production default is `true` (HTTPS); local HTTP dev requires `false` or browsers won't send auth cookies
- `ACCESS_SECRET` in xhttp/xws must equal `AUTH_ACCESS_SECRET` in xauth — they share the same HMAC key

## Deployment

Services are deployed via **Nomad with raw exec driver** (no Docker). Each node runs Nomad in hybrid server+client mode.

CI/CD flow:
- **Auto-deploy**: push to `main` → `ci.yml` runs build/vet/test → if pass, builds release binaries, creates GitHub pre-release (`build-N`), SSHes to prod server, runs `git pull` + `nomad job run`. All secrets come from GitHub Secrets (never from files on disk).
- **Versioned release**: push tag `v*` → `release.yml` creates a stable GitHub Release for manual/rollback deploys.
- **VPS setup**: single command on the new server — `wget | PLATFORM_DOMAIN=... NATS_USER=... bash`. Script auto-detects IP, configures swap, installs Nomad+NATS, sets up systemd+firewall, auto-joins cluster via DNS. Same command for first node and any subsequent node.

Rollback: set `version = "build-N"` (or `v1.2.3`) in `prod.vars` and run `nomad job run` manually.

Key Nomad behaviors:
- `/health` endpoint used by Nomad for self-healing — Gateway returns `200` if NATS is up, `503` otherwise; Nomad restarts on failure
- Rolling update: Nomad restarts tasks one at a time → zero-downtime deploys
- Log rotation: `logs { max_files = 5, max_file_size = 10 }` in job file — no external log agents needed
- `ReconnectConfig.MaxAttempts = -1` (infinite reconnect) — Nomad handles process lifecycle, not the app
- x-сервисы разворачиваются в `count = min(NODES, 3)` копий с `distinct_hosts` — при 1-2 нодах по копии на каждую, при 3+ ровно 3 копии. `NODES` (число ready-нод) вычисляется на prod-сервере через Nomad API `/v1/nodes` в момент `nomad job run` — CI об этом числе не знает. Gateway `type = "system"` — копия на каждой ноде по построению.

NATS cluster (production) uses DNS-based route discovery via `deployments/infra/nats/nats.conf`. Services connect to local NATS on `127.0.0.1:4222`.

Nomad configs: `deployments/infra/nomad/nomad.hcl` (agent), `deployments/infra/nomad/platform.nomad` (platform job), `deployments/infra/nomad/xservices.nomad` (demo services job).

Dev mode: `deployments/envs/dev/` — Docker Compose для NATS/PostgreSQL, `dev.vars` для переменных, инструкции в `dev.md`.
Prod mode: `deployments/envs/prod/` — шаблон `prod.vars.example`, инструкции в `prod.md`.

Firewall ports required between nodes: 4222/TCP (NATS client), 6222/TCP (NATS cluster), 4646/TCP (Nomad HTTP API), 4647–4648/TCP+UDP (Nomad RPC/Serf).
