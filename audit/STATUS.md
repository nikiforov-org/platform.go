# Аудит платформы — рабочий файл

**Последний полный аудит:** 2026-04-27
**Формат:** чекбокс `- [ ]` open · `- [x]` closed · `- [~]` deferred / won't fix.
**Соглашение:** при закрытии пункта ставить `[x]`, добавлять краткую пометку `(закрыт: <что сделано>)` и commit hash или дату. Файл — источник правды о состоянии платформы. Если находка закрыта, но вскрылся регресс — переоткрывать в новой секции "Регрессии".

**Закрытые пункты прошлых волн:** [`CLOSED-2026-04.md`](CLOSED-2026-04.md) — архив за период 2026-04-17 → 2026-04-27 (волны 2026-04-17, 2026-04-25, 2026-04-26).

Быстрая навигация:
- [Platform (внутренняя платформа)](#platform)
- [CI/CD + Nomad + NATS infra](#cicd--infra)
- [Demo-сервисы (x*)](#demo-сервисы)
- [Frontend (xfrontend, демо)](#frontend)
- [Глобальные рекомендации](#глобальные-рекомендации)
- [Как работать с этим файлом](#как-работать-с-этим-файлом)

---

## Platform

Код: `cmd/gateway/`, `internal/platform/`, `internal/middleware/`, `internal/services/gateway/`, `utils/`.

### Critical

_(пусто)_

### High

_(пусто)_

### Medium

_(пусто)_

### Low

_(пусто)_

### Позитив (не трогать — сделано правильно)

1. Graceful shutdown последовательность: HTTP `Shutdown` → `close(stopCh)` → NATS `Drain` выстроена корректно.
2. WS ping/pong: `wsPingInterval < wsReadDeadline`, `SetPongHandler` сдвигает deadline — классический keep-alive.
3. WS connection counter через `atomic.Int64` без мьютекса.
4. `realIP` защищён `TrustedProxy` — нет тривиального spoof через `X-Forwarded-For`.
5. Rate limiter ограничен по памяти (`MaxIPs` + eviction) — нет unbounded growth.
6. NATS reconnect: `MaxAttempts=-1`, все handlers (Disconnect/Reconnect/Closed/Error) заданы.
7. `utils/cookie.go` использует `http.Request.Cookie` — корректный RFC 6265 parser.
8. `utils/reply.go` использует `Header.Add` для `Set-Cookie` — множество кук сохраняется.
9. Body limit `io.LimitReader(r.Body, 1MB)` — нет memory exhaustion.
10. Panic recovery в `middleware/recover.go` читает `X-Request-Id` из header и включает в лог.

---

## CI/CD + Infra

Код: `.github/workflows/`, `.github/actions/deploy-nomad/`, `deployments/infra/nomad/`, `deployments/infra/nats/`, `deployments/envs/`.

### Critical

_(пусто)_

### High

- [ ] **I-H8** — `deployments/infra/nomad/nomad.hcl` + HEREDOC `deployments/envs/prod/setup.sh` — Nomad RPC (4647) и Serf (4648) идут между нодами в открытом виде. Nomad RPC несёт Raft-консенсус и job specs; job specs содержат реальные секреты: `NATS_PASSWORD`, `AUTH_ACCESS_SECRET`, `AUTH_REFRESH_SECRET`, `DATABASE_URL` — всё, что передаётся через `-var-file` или `env {}`-блоки в `.nomad`. Serf — gossip-протокол: список нод, статусы, leadership-сигналы. При развёртывании нод в разных ДЦ через публичный интернет атакующий с MITM перехватывает секреты непрерывно (не только в момент деплоя, как в I-H7, а 24/7). Последствия: `AUTH_ACCESS_SECRET` → подделка JWT-токенов (полный обход авторизации); `DATABASE_URL` → прямой доступ к PostgreSQL; `NATS_PASSWORD` → подключение к NATS-клиентскому порту (4222). NATS кластерный трафик (6222) уже защищён mTLS — оба слоя независимы и не дублируют друг друга. Fix (вариант 1, выбран): RPC через TLS + Serf через gossip-key (это два разных механизма Nomad — Serf через TLS не работает, у него собственное симметричное шифрование). В `setup.sh` добавить генерацию Nomad CA + node cert по симметрии с NATS (новые входные переменные `NOMAD_CA_KEY`/`NOMAD_CA_CERT`, аналогично `NATS_CA_KEY`/`NATS_CA_CERT`); SAN node-cert: `DNS:server.global.nomad,DNS:client.global.nomad,IP:NODE_IP,IP:127.0.0.1` (DNS-имена нужны для `verify_server_hostname=true` — Nomad валидирует cert пира по этому SAN, IP не используется). В `nomad.hcl` (+ HEREDOC) добавить `tls { rpc = true; verify_server_hostname = true; ca_file/cert_file/key_file }` и `server { encrypt = "${NOMAD_GOSSIP_KEY}" }`. Третья новая входная переменная — `NOMAD_GOSSIP_KEY` (32 байта в base64, `openssl rand -base64 32`). HTTP API (4646) остаётся plain (`tls.http = false`, default) — он на 127.0.0.1, в threat model cross-DC WAN MITM не входит; trade-off: все локальные curl/nomad CLI не требуют cert env vars (ACL bootstrap, healthcheck wait-loop, deploy-action probe). Синхронизировать оба места (правило `feedback_setup_heredoc_drift`); добавить `NOMAD_CA_KEY`/`NOMAD_CA_CERT`/`NOMAD_GOSSIP_KEY` в `setup.yml` (через GitHub Secrets); инструкцию генерации Nomad CA + gossip-key в `prod.md` (аналогично блоку NATS CA), новый раздел «Безопасность кластера Nomad» по симметрии с «Безопасность кластера NATS»; убрать из `prod.md` упоминание multi-tenant (система однокластерная — может включать ноды из разных ДЦ, но не несколько изолированных кластеров).

### Medium

_(пусто)_

### Low

_(пусто)_

### Позитив

1. Checksum-верификация: CI вычисляет SHA256 каждого архива, публикует через `actions/upload-artifact` с привязкой к `run-id`; `.nomad`-файлы используют `var.CHECKSUM` — Nomad artifact проверяет хеш.
2. Generic composite action: один `.github/actions/deploy-nomad/action.yml` для всех сервисов; `ci.yml` не знает имён сервисов — auto-discover из `cmd/`.
3. NATS mTLS: cluster-трафик (6222) защищён mTLS; CA-ключ не остаётся на серверах (удаляется после подписи cert).
4. `auto_revert = true` + `healthy_deadline = 3m` в каждом `.nomad`.
5. Идемпотентность `setup.sh`: swap, Nomad, NATS — все функции проверяют текущее состояние перед установкой.
6. Nomad ACL bootstrap идемпотентен: `|| true` + проверка `/v1/acl/self` подтверждает работоспособность токена.
7. Переменные в `.nomad` — ВЕРХНИЙ РЕГИСТР, совпадают с именами GitHub Secrets → прямой маппинг без трансформации.
8. Один `.nomad` на сервис — независимый деплой.
9. Dev-окружение (`start.sh`) генерирует inline-джобы с идентичной prod структурой блоков update/restart/logs.

---

## Demo-сервисы

Код: `cmd/{xauth,xhttp,xws}/`, `internal/services/{xauth,xhttp,xws}/`.

**Напоминание:** x* — демо. Production-hardening не требуется. Но найденные баги влияют на демо-воспроизводимость → фиксятся, если стоимость фикса не подразумевает новой платформенной зависимости.

### Critical

_(пусто)_

### High

_(пусто)_

### Medium

_(пусто)_

### Low

_(пусто)_

### Позитив

1. `hmac.Equal` в `HandleLogin` и `VerifyJWT` — timing-safe compare.
2. Refresh revocation через KV реализована семантически правильно (`GetValue` → `nil || "revoked"` трактуется как отозван).
3. Graceful shutdown: все сервисы выполняют `Drain`, xws дополнительно `CloseAll()` до Drain.
4. SQL полностью параметризован (`$1, $2, $3`) — инъекции исключены.
5. Изолированные KV-бакеты (`authms_refresh_tokens`, `xhttp_cache`) — нет коллизий ключей между сервисами.

---

## Frontend

Код: `xfrontend/` — React + Vite клиент для ручного тестирования демо-сервисов (xauth/xhttp/xws). По соглашению префикса `x*` — демо, замещается в реальных проектах. Платформенный scope — только в части (а) корректного протокола взаимодействия с Gateway (cookies, WS handshake) и (б) синхронизации с backend-API.

### Critical

_(пусто)_

### High

_(пусто)_

### Medium

_(пусто)_

### Low

_(пусто)_

### Позитив

1. `apiCall` использует `credentials: 'include'` — HttpOnly-куки auth работают через Vite dev-proxy без CORS (same-origin).
2. `wsURL` корректно меняет `http(s)` → `ws(s)` и поддерживает оба режима (proxy / абсолютный URL).
3. `xfrontend/.gitignore` исключает `node_modules/`, `dist/`, `.env`, `.env.local`, `*.log`. `git ls-files xfrontend/` подтверждает: ни сборочные артефакты, ни секреты не попадают в репозиторий.
4. `useEffect(() => () => wsRef.current?.close(), [])` в `Ws.tsx` — корректный cleanup на unmount, нет утечки WebSocket-соединений при переключении вкладок / unmount страницы.
5. Минимальный набор зависимостей (`react`, `react-dom`, `vite`, `typescript`) — ограниченный supply-chain attack surface.
6. `tsconfig.json` с `strict: true` — TypeScript ловит большинство runtime-ошибок на этапе сборки.

---

## Глобальные рекомендации

Вне конкретных файлов — общая гигиена:

_(пусто)_

---

## Регрессии

Регрессии открываются при возврате ранее закрытого пункта в неверное состояние. ID — `<original>-regression`. Закрытие — пометить `[x]` с датой и кратким описанием фикса.

_(пусто)_

---

## Как работать с этим файлом

**При старте новой сессии:**
1. Открыть этот файл.
2. Искать `- [ ]` по разделам — это активные пункты.
3. Приоритет: Critical > High > Medium > Low. Внутри категории порядок не важен.
4. ID вида `P-C1`, `I-H2`, `D-M3` — стабильные ссылки. При обсуждении пункта использовать ID.

**При закрытии пункта:**
1. `- [ ]` → `- [x]`.
2. Добавить в конец строки: ` (закрыт 2026-04-XX: краткое описание фикса)`.
3. Если фикс повлиял на другие пункты — переоткрыть их.

**При открытии новой находки:**
1. Присвоить ID по схеме `<область>-<приоритет><номер>`: области `P/I/D/G`, приоритеты `C/H/M/L`, номер — следующий свободный по архиву (см. `CLOSED-2026-04.md`).
2. Формат: `` - [ ] **<ID>** — `file:line` — описание. Fix: решение.``

**Регрессии:**
Если закрытый пункт снова стал актуален — НЕ переоткрывать `[x]`. Создать новую запись `<ID>-regression` с описанием регрессии и датой.

**Полный ре-аудит:**
Раз в 2 недели (или после крупного рефакторинга): архивировать закрытые пункты в `CLOSED-YYYY-MM.md`, очистить разделы, обновить "Последний полный аудит" в шапке.
