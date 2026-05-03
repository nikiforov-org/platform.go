# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Environment Variables Naming Convention

Все переменные окружения в проекте следуют строгому соглашению о префиксах:

**Платформенные переменные (префикс `PLATFORM_`):**
- Относятся к инфраструктуре платформы: NATS, Nomad, Gateway, HTTP-сервер
- Примеры: `PLATFORM_NATS_USER`, `PLATFORM_GATEWAY_RATE_LIMIT`, `PLATFORM_HTTP_ADDR`
- Используются во всех окружениях (dev, prod)

**X-сервисы (префикс `X_`):**
- Относятся к демонстрационным сервисам (xauth, xhttp, xws)
- Примеры: `X_AUTH_PASSWORD`, `X_HTTP_DATABASE_URL`, `X_WS_INACTIVITY_TIMEOUT`
- Не являются частью платформы — только примеры использования

**GitHub Secrets vs Variables:**
- **Secrets** — криптоматериалы, пароли, токены, приватные ключи
  - `PLATFORM_DEPLOY_SSH_KEY`, `PLATFORM_NATS_PASSWORD`, `X_AUTH_ACCESS_SECRET`
- **Variables** — публичные настройки, домены, имена пользователей, таймауты
  - `PLATFORM_DOMAIN`, `PLATFORM_ALLOWED_HOSTS`, `X_AUTH_USERNAME`

При добавлении новой переменной:
1. Определи категорию (платформа/X-сервис)
2. Выбери правильный префикс (`PLATFORM_` или `X_`)
3. Определи тип (secret/variable)
4. Добавь во все необходимые файлы (Go config, Nomad job, workflow, документация)

## Audit status

Единый рабочий файл аудита платформы: [`audit/STATUS.md`](audit/STATUS.md). Читать в начале любой сессии, связанной с правкой/разбором кода. Источник правды о состоянии открытых (`- [ ]`) и закрытых (`- [x]`) находок. Приоритет: Critical > High > Medium > Low. ID находок стабильные (`P-C1`, `I-H2`, `D-M3`, `G4` — P=platform, I=infra/CI, D=demo, G=global). После фикса — поменять чекбокс на `[x]` с пометкой даты и краткого описания, в том же файле.

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

```bash
# Unit tests с race detector (требует CGO; ubuntu-latest и macOS — ок).
go test -race ./...

# Integration тесты — embedded NATS (github.com/nats-io/nats-server/v2/test),
# поднимаются прямо в test-процессе, без docker/compose.
go test -race -tags=integration ./...

# Один тест.
go test -v -race -run TestName ./internal/services/gateway/...
```

**Структура:**
- `_test.go` рядом с кодом — Go-стандарт.
- Unit-тесты: без build-тегов.
- Integration-тесты: build tag `//go:build integration` в первой строке файла.
- Postgres-зависимые тесты будут поднимать сервис через GHA `services: postgres:17` (когда появятся).
- `internal/testutil/` создаётся, когда хелпер (embedded NATS, JWT-генератор, fixture-loader) дублируется в 3+ пакетах.

**Линтер:**
- `_test.go` — errcheck отключён, gosec G404|G101 отключены (см. `.golangci.yml`).
- В тестах допустимы `defer rows.Close()` без проверки и `math/rand` для test-данных.

## Metrics

Prometheus-метрики Gateway отдаются через отдельный HTTP-эндпоинт на loopback:

```bash
# По умолчанию — 127.0.0.1:8081 (PLATFORM_GATEWAY_METRICS_ADDR).
curl http://127.0.0.1:8081/metrics
```

Bind на loopback осознанно: на prod-сервере `/metrics` достижим через тот же SSH-tunnel, что и Nomad UI (`4646`); внешняя экспозиция не нужна, multi-DC агрегация — через Prometheus federation.

Платформенные метрики (`internal/platform/metrics/`):
- `gateway_http_requests_total{service,method,status}` — Counter, общее число HTTP-запросов через Gateway.
- `gateway_http_request_duration_seconds{service,method}` — Histogram, end-to-end длительность.
- `gateway_ws_connections_active` — Gauge, текущее число открытых WS.
- `gateway_rate_limit_rejected_total{kind}` — Counter, отклонённые rate limiter (kind=`general`|`auth`).
- `nats_request_duration_seconds{service}` — Histogram, NATS Request-Reply round-trip.
- `nats_request_attempts_total{service,outcome}` — Counter, попытки NATS-запроса (outcome=`ok`|`no_responders`|`timeout`|`canceled`|`error`).

Бесплатно от `prometheus/client_golang`: `go_*` (память, GC, goroutines), `process_*` (CPU, FDs).

## Demo vs. Platform Code

Services prefixed with `x` — **xhttp, xauth, xws** — are **demo examples only**. They exist to demonstrate platform structure, patterns, and capabilities. They are not production-ready and will be replaced by real business services in actual projects built on this platform. Do not spend effort on production-quality hardening of x* services.

The platform-layer code (Gateway, `internal/platform/nc`, `internal/middleware`, `utils`) is the real subject of this repository.

## Architecture

This is a **microservices platform** where all inter-service communication runs over NATS Pub/Sub. The traffic flow is:

```
HTTP Client → Gateway (:80) → NATS (:4222) → [xhttp | xauth | xws]
```

**Gateway** (`internal/services/gateway/`) is the sole HTTP entry point. Routes:
- `GET /health` — health check (bypasses rate limiting and Origin); `200` if NATS connected, `503` otherwise.
- `/v1/{service}/{method}` — proxies HTTP → NATS Request-Reply.
- `/v1/{service}/ws` — upgrades to WebSocket, bridges to NATS Pub/Sub.

Middleware chain for `/v1/`: `Origin` → `RateLimit` → route.

Rate limiting (`internal/services/gateway/ratelimit.go`): per-IP general limit (`PLATFORM_GATEWAY_RATE_LIMIT`, default 100 req/s) applies to all routes; optional stricter per-IP auth limit (`PLATFORM_GATEWAY_AUTH_RATE_LIMIT`, default 5 req/s) applies to a configurable URL prefix (`PLATFORM_GATEWAY_AUTH_RATE_PREFIX`, default empty — disabled); global WS connection counter (`PLATFORM_GATEWAY_MAX_WS_CONNS`, default 1000).

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
- `utils/hosts.go` — CORS origin validation against `PLATFORM_ALLOWED_HOSTS`

### Database

PostgreSQL is used only by xhttp. No ORM — raw `database/sql` with parameterized queries. The `xhttp` table has: `id` (BIGSERIAL), `name` (TEXT), `value` (TEXT), `created_at`, `updated_at`.

Connection limits in `cmd/xhttp/main.go`: max 10 open, 5 idle, 5-minute lifetime.

## Configuration

All configuration is loaded from environment variables only. Each service has a `LoadConfig()` in its package.

**Required variables by service:**

| Service | Required Env Vars |
|---------|-------------------|
| xhttp   | `X_HTTP_DATABASE_URL`, `X_AUTH_ACCESS_SECRET` |
| xauth   | `X_AUTH_USERNAME`, `X_AUTH_PASSWORD`, `X_AUTH_ACCESS_SECRET`, `X_AUTH_REFRESH_SECRET` |
| gateway | `PLATFORM_ALLOWED_HOSTS` (comma-separated origins, e.g. `localhost:3000,example.com`) |

All services share `PLATFORM_NATS_HOST` (default `127.0.0.1`), `PLATFORM_NATS_PORT` (default `4222`), `PLATFORM_NATS_USER`, `PLATFORM_NATS_PASSWORD`, `PLATFORM_LOG_LEVEL` (default `info`; values: `debug`, `info`, `warn`, `error`).

Gateway ретраит NATS-запросы при `ErrNoResponders` (нет живых подписчиков) в пределах `PLATFORM_GATEWAY_NATS_REQUEST_TIMEOUT`, с паузой `PLATFORM_GATEWAY_NATS_RETRY_DELAY` (default `100ms`) между попытками. Это закрывает короткое окно при перетасовке копий сервиса на другой ноде; прочие ошибки возвращаются клиенту без повторов.

`NATS_KV_REPLICAS` не задаётся — платформа определяет число реплик автоматически по размеру кластера (`len(conn.Servers())`) после подключения к NATS.

**Important dev-only overrides:**
- `X_AUTH_COOKIE_SECURE=false` — production default is `true` (HTTPS); local HTTP dev requires `false` or browsers won't send auth cookies
- `X_AUTH_ACCESS_SECRET` is shared between xauth and xhttp — same HMAC key, same env var name

## Deployment

Services are deployed via **Nomad with raw exec driver** (no Docker). Each node runs Nomad in hybrid server+client mode.

CI/CD flow:
- **Auto-deploy**: push to `main` → `ci.yml` runs build/vet/test → if pass, builds release binaries, creates GitHub pre-release (`build-N`), SSHes to prod server, runs `git pull` + `nomad job run`. All secrets come from GitHub Secrets (never from files on disk).
- **Versioned release**: push tag `v*` → `release.yml` creates a stable GitHub Release for manual/rollback deploys.
- **VPS setup**: single command on the new server — `wget | PLATFORM_DOMAIN=... PLATFORM_NATS_USER=... bash`. Script auto-detects IP, configures swap, installs Nomad+NATS, sets up systemd+firewall, auto-joins cluster via DNS. Same command for first node and any subsequent node.

Rollback: set `version = "build-N"` (or `v1.2.3`) in `prod.vars` and run `nomad job run` manually.

Key Nomad behaviors:
- `/health` endpoint used by Nomad for self-healing — Gateway returns `200` if NATS is up, `503` otherwise; Nomad restarts on failure
- Rolling update: Nomad restarts tasks one at a time → zero-downtime deploys
- Log rotation: `logs { max_files = 5, max_file_size = 10 }` in job file — no external log agents needed
- `ReconnectConfig.MaxAttempts = -1` (infinite reconnect) — Nomad handles process lifecycle, not the app
- x-сервисы разворачиваются в `count = min(NODES, 3)` копий с `distinct_hosts` — при 1-2 нодах по копии на каждую, при 3+ ровно 3 копии. `NODES` (число ready-нод) вычисляется на prod-сервере через Nomad API `/v1/nodes` в момент `nomad job run` — CI об этом числе не знает. Gateway `type = "system"` — копия на каждой ноде по построению.

**NATS clustering:** `setup.sh` installs nodes in standalone mode — JetStream works immediately. GitHub Actions workflow (`clustering.yml`) manages cluster formation automatically when 2+ nodes exist in DNS. When adding a node: run `setup.sh` (node starts standalone) → push to `main` or manually trigger `clustering.yml` → workflow detects all nodes, creates `/etc/nats/cluster.conf`, does `systemctl reload nats` → cluster formed. Services connect to local NATS on `127.0.0.1:4222`.

**Any binary as a service:** Nomad's `raw_exec` driver runs any static Linux binary — Go, Rust, or any language with static linking. The binary connects to local NATS on `127.0.0.1:4222`, subscribes to subjects, and becomes a full platform participant. It does not have to be built by this repo's CI — any URL with a checksum in the `artifact {}` block works. Docker is not required and not installed.

**Single cluster, multi-DC:** the platform runs as one Nomad+NATS cluster; nodes can span multiple datacenters. `raft_multiplier=5` accommodates WAN latency. Inter-node traffic is encrypted end-to-end: NATS cluster (6222) via mTLS (`PLATFORM_NATS_CA_KEY`/`PLATFORM_NATS_CA_CERT`), Nomad RPC (4647) via TLS (`PLATFORM_NOMAD_CA_KEY`/`PLATFORM_NOMAD_CA_CERT`), Nomad Serf gossip (4648) via symmetric key (`PLATFORM_NOMAD_GOSSIP_KEY`).

**Multiple isolated clusters** (separate products/tenants): each cluster needs its own `PLATFORM_DOMAIN` (Nomad `retry_join` boundary), its own `PLATFORM_NATS_CA_KEY`/`PLATFORM_NATS_CA_CERT` (NATS mTLS isolation), and its own `PLATFORM_NOMAD_CA_KEY`/`PLATFORM_NOMAD_CA_CERT`/`PLATFORM_NOMAD_GOSSIP_KEY` (Nomad TLS+Serf isolation). Nodes with different CA certs cannot establish cross-cluster connections — CA-pair separation is the security boundary. The same `setup.sh` is used for every cluster, only the variable values differ.

Nomad configs: `deployments/infra/nomad/nomad.hcl` (agent), `deployments/infra/nomad/platform.nomad` (platform job), `deployments/infra/nomad/xservices.nomad` (demo services job).

Dev mode: `deployments/envs/dev/` — Docker Compose для NATS/PostgreSQL, `dev.vars` для переменных, инструкции в `dev.md`.
Prod mode: `deployments/envs/prod/` — шаблон `prod.vars.example`, инструкции в `prod.md`.

Firewall ports required between nodes: 4222/TCP (NATS client), 6222/TCP (NATS cluster), 4646/TCP (Nomad HTTP API), 4647–4648/TCP+UDP (Nomad RPC/Serf). 