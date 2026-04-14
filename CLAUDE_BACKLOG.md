# Backlog

## Аудит — 2026-04-14

### Сборка и vet

`go build ./...` — чисто  
`go vet ./...` — чисто  
Незакоммиченных изменений — нет

---

### Ошибки

#### ~~[BUG] xhttp: отсутствует PostgreSQL-драйвер~~ — исправлено

Добавлен `github.com/lib/pq v1.12.3` в `go.mod` и `_ "github.com/lib/pq"` в `cmd/xhttp/main.go`.

---

### Замечания

#### ~~[NOTE] utils: stdlib log вместо zerolog~~ — исправлено

`utils/log.go` — добавлен пакетный `zerolog.Logger` (по умолчанию `nop`).  
`utils.SetLogger(log)` вызывается в каждом `cmd/*/main.go` сразу после `logger.New`.  
`utils/reply.go`, `utils/hosts.go`, `utils/env.go` — переведены на zerolog.

#### ~~[NOTE] Демо-сервисы: stdlib log при старте~~ — исправлено

`xauth/config.go`, `xhttp/config.go` — `log.Fatalf` / `log.Fatal` заменены на `utils.Logger().Fatal()`.

#### ~~[NOTE] middleware/xauth.go: дублирование verifyJWT~~ — исправлено

`internal/middleware/xauth.go` — демо-middleware, не платформенный. Переведён на `xauth.VerifyJWT` и `xauth.Claims`. Дублирование устранено.

#### ~~[NOTE] gateway/handlers.go: позиция defer r.Body.Close()~~ — исправлено

`defer r.Body.Close()` перемещён до `io.ReadAll`.

---

### Состояние компонентов

#### Платформа

| Компонент | Статус |
|---|---|
| `internal/platform/nc/` | готово |
| `internal/platform/logger/` | готово |
| `internal/middleware/recover.go` | готово |
| `internal/middleware/xauth.go` | готово |
| `internal/services/gateway/` | готово |
| `utils/` | готово |
| Graceful shutdown (NATS Drain) | готово |
| Изоляция x* из платформенного кода | готово |

#### Демо-сервисы

| Сервис | Статус |
|---|---|
| xauth | готово |
| xws | готово |
| xhttp | не запустится (см. BUG выше) |

---

### Что не реализовано

- Тесты: отсутствуют полностью
- Реальные бизнес-сервисы: не реализованы (x* — только демо)
