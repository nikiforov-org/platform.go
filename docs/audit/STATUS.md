# Аудит платформы — рабочий файл

**Последний полный аудит:** 2026-04-17
**Формат:** чекбокс `- [ ]` open · `- [x]` closed · `- [~]` deferred / won't fix.
**Соглашение:** при закрытии пункта ставить `[x]`, добавлять короткую пометку `(закрыт: <что сделано>)` и commit hash или дату. Файл — источник правды о состоянии платформы. Если находка закрыта, но вскрылся регресс — переоткрывать в новой секции "Регрессии".

Быстрая навигация:
- [Platform (внутренняя платформа)](#platform)
- [CI/CD + Nomad + NATS infra](#cicd--infra)
- [Demo-сервисы (x*)](#demo-сервисы)
- [Глобальные рекомендации](#глобальные-рекомендации)
- [Как работать с этим файлом](#как-работать-с-этим-файлом)

---

## Platform

Код: `cmd/gateway/`, `internal/platform/`, `internal/middleware/`, `internal/services/gateway/`, `utils/`.

### Critical

- [x] **P-C1** — `go.mod:3` объявляет `go 1.26.1`. ~~Релиз не существует~~ (закрыт 2026-04-17: ложная находка аудитора из-за training cutoff — Go 1.26 релизнут в феврале 2026, версия валидна, CI использует `go-version-file: go.mod` корректно).
- [x] **P-C2** — `internal/services/gateway/handlers.go:220` — NATS `RequestMsg` игнорирует `r.Context()` и фиксированный 5s таймаут (закрыт 2026-04-17: `RequestMsgWithContext` с `WithTimeout(r.Context(), cfg.HTTP.NATSRequestTimeout)`, добавлен `GATEWAY_NATS_REQUEST_TIMEOUT` env/default 5s, `http.Server.BaseContext = gw.RootContext()` в main — shutdown теперь отменяет все in-flight запросы; ошибки классифицируются в 499/504/503 через `natsRequestErrStatus`).
- [x] **P-C3** — `internal/services/gateway/handlers.go:367-376` — race NATS-коллбэк vs `conn.Close()` (закрыт 2026-04-17: `safeWrite` проверяет `ctx.Done()` под mu; `conn.Close()` вызывается под тем же mu через объединённый defer `cancel → Lock → Close` — последним в LIFO после `sub.Unsubscribe`; NATS-коллбэк и теперь содержит быстрый `ctx.Err()` exit до mu, чтобы при массовом shutdown не толпиться на lock).
- [x] **P-C4** — `internal/services/gateway/handlers.go:325-329` — при ошибке `rand.Read` для sessionID функция возвращается без отправки CloseMessage клиенту (закрыт 2026-04-17: генерация `sessionID` перенесена ДО `Upgrade`; при сбое CSPRNG отдаётся `http.Error 500` + `releaseConn()`, после `Upgrade` точка отказа исключена — handler работает в едином инварианте `conn != nil ⇒ rand отработал`).

### High

- [x] **P-H1** — `internal/services/gateway/ratelimit.go:77-84` — `evictOldest` выполняет O(N) линейный скан таблицы IP под `mu` (закрыт 2026-04-17: переход на настоящий LRU — `ipTable` = `map + container/list.List`, у `ipEntry` появился `elem *list.Element`. На hit `MoveToFront(elem)` — O(1). При переполнении удаляется `lst.Back()` — O(1) вместо O(N) сканирования. Cleanup тоже O(K) вместо O(N) — back-walk по списку до первой неэкспирированной записи. Self-amplification под DDoS с уникальных IP снят, время под `mu` константное независимо от `MaxIPs`).
- [x] **P-H2** — `cmd/gateway/main.go:64` — `log.Fatal()` вызывается внутри горутины (закрыт 2026-04-17: ошибка `ListenAndServe` направляется в buffered `serverErr`-канал; main через `select` ловит либо SIGINT/SIGTERM, либо server error, и в обоих случаях идёт по единому shutdown path — `close(stopCh)` → `server.Shutdown` → `natsClient.Drain`. Non-zero `exitCode` сохраняется и вызывается `os.Exit(1)` в самом конце для Nomad restart logic; `cancel()` shutdownCtx вызывается явно, так как defer не сработает после os.Exit).
- [x] **P-H3** — `internal/platform/nc/client.go:336-343` — `Drain` реализован busy-wait циклом (закрыт 2026-04-17: переход на event-driven — в `PlatformClient` добавлено поле `closed chan struct{}`, в `nats.ClosedHandler` (уже зарегистрированном) добавлен `close(closed)`. `Drain` теперь использует `select { case <-p.closed: ...; case <-time.After(timeout): ... }` — ноль задержки на детект завершения, ровно один источник истины о закрытии соединения. Hard-close по таймауту тоже триггерит ClosedHandler, но `Drain` уже ушёл с ошибкой; повторное закрытие канала не происходит, т.к. close выполняется только в одном месте — handler'е).
- [x] **P-H4** — `internal/middleware/xauth.go:87` — ответ собирается конкатенацией (закрыт 2026-04-17: переход на `json.Marshal(struct{Error string})` — корректный escape любых спецсимволов в `text`, защищён от injection при будущей передаче динамических значений; error от `json.Marshal` для `struct{string}` безошибочен и игнорируется явным `_`).
- [ ] **P-H5** — `internal/middleware/xauth.go:64` — двойная проверка `exp`: `xauth.VerifyJWT` её уже делает, затем ручной `time.Now().Unix() > c.Exp`. Проверка без clock-skew tolerance и не атомарна относительно Verify. Fix: убрать дублирование, clock skew (±60s) вложить внутрь VerifyJWT.
- [ ] **P-H6** — `internal/services/gateway/handlers.go:111,116` — `w.Write()` в health-check игнорирует ошибку без пометки. Fix: либо обрабатывать, либо явный `_, _ =` с комментарием.

### Medium

- [ ] **P-M1** — `internal/services/gateway/handlers.go:160` — маршрут `/v1/{service}/ws` не проверяет `r.Method`. WS upgrade принимает POST/PUT. Fix: `if r.Method != http.MethodGet { http.Error(...) }`.
- [ ] **P-M2** — `internal/services/gateway/handlers.go:186-188` — `service` и `methodParts` идут в NATS subject без санитизации. Символы `*`, `>`, пробел — спецсимволы NATS, позволяют манипулировать маршрутизацией. Fix: валидация через regex `^[a-zA-Z0-9_-]+$` для каждого сегмента.
- [ ] **P-M3** — `internal/services/gateway/ratelimit.go:184-197` — `realIP` возвращает `X-Real-IP` как есть, без `net.ParseIP`. Скомпрометированный или misconfigured прокси может передать пустую строку или мусор → все запросы учитываются под одним ключом, rate limit обходится. Fix: `if ip := net.ParseIP(v); ip != nil { return ip.String() }`.
- [ ] **P-M4** — `internal/platform/logger/logger.go:31` — `zerolog.SetGlobalLevel` — глобальный side-effect в фабрике. Каждый вызов `logger.New()` перезаписывает уровень для всех логгеров процесса. Fix: выставлять уровень через `logger.Level(level)` на конкретном экземпляре.
- [ ] **P-M5** — `internal/services/gateway/handlers.go:419-426` — нет `conn.SetReadLimit(...)`. gorilla defaults (32KB) легко насыщаются большими фреймами. Fix: `conn.SetReadLimit(64 * 1024)` после Upgrade.
- [ ] **P-M6** — `utils/log.go:8-13` — глобальная `pkgLog` без защиты (ни atomic, ни mutex). Сейчас `SetLogger` вызывается строго до старта горутин, но гарантии нет. Fix: `atomic.Pointer[zerolog.Logger]`.
- [ ] **P-M7** — `internal/services/gateway/handlers.go:351-363` — connect-сообщение отправляется через `Publish` (fire-and-forget) без подтверждения. При отсутствии подписчика WS остаётся открытым, не работая. Fix: использовать `RequestMsg` для connect с 2s таймаутом, при отказе — WS close 503.
- [ ] **P-M8** — `internal/services/gateway/handlers.go:404-413` — shutdown-горутина запускается без `sync.WaitGroup`. При return из `handleWS` горутина может обращаться к уже закрытому `conn`. Fix: `errgroup.Group` или `sync.WaitGroup`.

### Low

- [ ] **P-L1** — `internal/services/gateway/handlers.go:257-262` — `Dur("ms", time.Since(start))` пишет наносекунды под ключом `ms`. Fix: `Int64("ms", time.Since(start).Milliseconds())`.
- [ ] **P-L2** — `internal/middleware/xauth.go:89-92` — ошибка `RespondMsg` молча игнорируется (комментарий "no logger available"). Если клиент не получил 401 — запрос утекает. Fix: передавать `zerolog.Logger` в `replyUnauthorized`.
- [ ] **P-L3** — `internal/platform/nc/client.go:255` — `CreateKeyValue` error treated as "already exists" для всех ошибок. Auth/network failures маскируются. Fix: `errors.Is(err, jetstream.ErrBucketExists)` явно.
- [ ] **P-L4** — `utils/hosts.go:108-112` — `AllowedHostsFromEnv` дублирует логику, используемую в `LoadConfig`. Dead code или лишний entry-point. Fix: удалить.

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

- [x] **I-C1** — `.github/actions/deploy-nomad/action.yml:85` — секреты передаются в `nomad job run` как `-var KEY=VALUE` в командной строке (закрыт 2026-04-17: `-var` флаги заменены на `export NOMAD_VAR_<NAME>=...` внутри SSH-скрипта; Nomad нативно читает эти env-переменные, значения попадают в `/proc/<pid>/environ` (читаемо только владельцем процесса), а не в world-readable cmdline; `printf '%q'` сохранён для безопасной shell-экранировки при интерполяции).
- [x] **I-C2** — `.github/actions/deploy-nomad/action.yml:37` — `echo "$DEPLOY_SSH_KEY" > /tmp/deploy_key` создаёт файл с дефолтным umask до строки 38 `chmod 600` (закрыт 2026-04-17: переход на `mktemp` + `trap EXIT` — файл создаётся атомарно с режимом `0600` под уникальным именем, race-окно на правах исчезло, symlink-attack на фиксированный путь `/tmp/deploy_key` тоже невозможен; финальный `rm -f /tmp/deploy_key` удалён, cleanup теперь гарантирован на любом пути выхода через trap).
- [x] **I-C3** — `.github/workflows/setup.yml` — script injection через `${{ inputs.* }}`-интерполяцию в bash-блоках (закрыт 2026-04-17: переформулирован — оригинальная находка про секреты неактуальна, secrets уже шли через `env:`-секцию ещё с коммита `3557643`. Реальная проблема — `${{ inputs.platform_domain }}`, `${{ inputs.node_ip }}` в heredoc/scp/ssh-блоках: heredoc `<<EOF` без кавычек выполняет `$()` в подставленном значении → RCE на runner при враждебном input. Фикс: все `inputs` перенесены в `env:` секции своих шагов, в скриптах используются `${PLATFORM_DOMAIN}`, `${NODE_IP}`, `${REPO_URL}`).

### High

- [ ] **I-H1** — `deployments/infra/nomad/nomad.hcl:32` + `deployments/envs/prod/setup.sh:177` — `bootstrap_expect = 1` на каждой ноде. Одновременный запуск двух нод → два одноузловых лидера до того, как `server_join` их объединит. Autopilot merge работает, но при сетевой изоляции в момент bootstrap возможен split-brain. Fix: при первоначальном развёртывании 3+ нод — либо поднимать ноды последовательно (задокументировать), либо использовать `bootstrap_expect = 3` для первого запуска.
- [ ] **I-H2** — `.github/workflows/{gateway,xauth,xhttp,xws}-deploy.yml` — все 4 workflow триггерятся `workflow_run: CI` параллельно, выполняют SSH на ту же ноду и `git pull` в одну и ту же `/opt/platform` → race condition. Fix: `concurrency: group: deploy-${{ github.sha }}, cancel-in-progress: false` во всех 4 workflow, либо `flock /opt/platform/.deploy.lock` на сервере.
- [ ] **I-H3** — `.github/actions/deploy-nomad/action.yml:85` — `nomad job run $VAR_ARGS` подвержен word-splitting. Значения с пробелами (например `ALLOWED_HOSTS=a.com, b.com`) сломают парсинг. Fix: то же решение что и I-C1 — переход на `NOMAD_VAR_*` env устраняет обе проблемы.
- [ ] **I-H4** — `deployments/envs/prod/setup.sh:265-272` — `wget https://github.com/nats-io/.../nats-server-....tar.gz` без SHA256 или GPG верификации. MITM или взломанный GitHub Release → подменённый бинарь. Fix: захардкодить `sha256sum -c` после скачивания для каждой версии.
- [ ] **I-H5** — `.github/workflows/setup.yml:76-79` — `scp -o StrictHostKeyChecking=no` на новый IP. При первом подключении fingerprint не сверяется → MITM получит `setup_env` с CA-ключом NATS. Fix: принимать `host_fingerprint` input и писать в `known_hosts` перед scp.
- [ ] **I-H6** — `deployments/envs/prod/setup.sh:237` (nomad.service) — нет `User=nomad`. Nomad работает от root, `raw_exec`-таски наследуют root. Fix: создать пользователя `nomad`, ограничить доступ к `/var/lib/nomad`, `/etc/nomad/env`, добавить `User=nomad` в systemd unit.

### Medium

- [ ] **I-M1** — `.github/workflows/ci.yml:124-130` — `gh release list | tail -n +$(( KEEP + 1 )) | xargs gh release delete` без `jq 'sort_by(.createdAt)'`. Порядок по имени: `build-9 > build-10` лексикографически → удаляются не старейшие. Fix: `gh release list --json name,createdAt | jq 'sort_by(.createdAt) | reverse | .[KEEP:]'`.
- [ ] **I-M2** — `.github/actions/deploy-nomad/action.yml:47` — DNS-перебор нод выбирает первую доступную по SSH, но не проверяет статус Nomad leader. Нода в процессе перезагрузки имеет живой SSH, но мёртвый Nomad. Fix: `curl -sf http://$HOST:4646/v1/status/leader` перед `nomad job run`.
- [ ] **I-M3** — `deployments/infra/nomad/gateway.nomad:64` — `count = 1` + `max_parallel = 1` → окно недоступности 10+ секунд при rolling update. Fix: `count = 2` для gateway, либо явно задокументировать accepted downtime.
- [ ] **I-M4** — `deployments/envs/prod/setup.sh:299` — `openssl genrsa 2048` с сертификатом на `days 3650`. NIST рекомендует RSA-3072+ или ECDSA P-256 для 10-летнего срока. Fix: `openssl ecparam -name prime256v1 -genkey -out node.key`.
- [ ] **I-M5** — `deployments/envs/prod/setup.sh:293-327` — CA-ключ пишется в `/tmp/nats-ca.key`. Удаляется `rm -f` вместо `shred -u`. При OOM/crash в окне между созданием и удалением ключ остаётся на диске. Fix: `shred -u`, или использовать process substitution `<(...)` без создания файла.
- [ ] **I-M6** — `deployments/envs/prod/prod.vars.example` — не содержит `INACTIVITY_TIMEOUT` (xws), `CACHE_TTL` (xhttp), `AUTH_ACCESS_TTL`/`AUTH_REFRESH_TTL` (xauth), `LOG_LEVEL`. При ручном `nomad job run -var-file` они остаются на дефолтах без напоминания. Fix: добавить все объявленные в `.nomad`-файлах переменные с комментарием про дефолт.
- [ ] **I-M7** — `deployments/envs/prod/prod.vars.example:11-18` ссылается на `platform.nomad` / `xservices.nomad`, которых не существует (разделены на gateway.nomad + xauth.nomad + xhttp.nomad + xws.nomad). Fix: обновить примеры команд.
- [ ] **I-M8** — `deployments/envs/dev/dev.md:49-65` — те же ссылки на устаревшие имена джобов. Fix: обновить.
- [x] **I-M9** — `.github/workflows/setup.yml` передавал `INSTALL_POSTGRES`, но `setup.sh` его никогда не читал (закрыт 2026-04-17 вместе с I-C3: input `install_postgres` и его пропуск в env-файл удалены целиком. PostgreSQL — зависимость демо-сервиса xhttp, в платформенном CI ему места нет; настройка БД остаётся за демо-руководством. Аудитор ошибся в номере строки — на `setup.sh:39` находится `NOMAD_TOKEN`, не `INSTALL_POSTGRES`; в setup.sh переменная вообще никогда не упоминалась).
- [ ] **I-M10** — `.github/actions/deploy-nomad/action.yml:93-101` — `NOMAD_TOKEN` проходит `printf '%q'` и вставляется в `bash -c "..."` SSH-аргумент. При включённом `sshd` verbose на сервере или `set -x` на клиенте токен попадает в логи. Fix: передавать через stdin pipe (`ssh ... 'bash -s' <<< "$script"` с экспортом env через `export NOMAD_TOKEN=$TOKEN` в начале, где `$TOKEN` подставляется в heredoc с кавычками `<<"EOF"`).

### Low

- [ ] **I-L1** — `deployments/infra/nats/nats.conf` — cluster-блок защищён только mTLS, без `cluster { authorization { user/password } }`. При компрометации одного cert можно подключиться без пароля. Fix: добавить cluster authorization или зафиксировать в угрозах что mTLS достаточно.
- [ ] **I-L2** — `deployments/infra/nomad/nomad.hcl` — нет `raft_multiplier`. Для multi-region или нестабильной сети рекомендуется `raft_multiplier = 5`. Fix: добавить при расширении на multi-DC.
- [ ] **I-L3** — `deployments/envs/prod/setup.sh:460-465` — `sleep 2` между запуском NATS и Nomad. При тяжёлом JetStream recovery ненадёжно. Fix: `until nats-server --signal ldm 2>/dev/null; do sleep 1; done` или `systemctl is-active nats`.
- [ ] **I-L4** — `.github/workflows/ci.yml:82,96` — `ls cmd/` выдаёт файлы вместе с директориями. Любой файл в `cmd/` (README, .gitkeep) сломает build. Fix: `find cmd -maxdepth 1 -mindepth 1 -type d -printf '%f\n'`.
- [ ] **I-L5** — `deployments/envs/prod/setup.sh` — `install_nomad` пропускает установку если `nomad` уже в PATH, без проверки версии. Повторный запуск не обновит. Fix: сравнить `nomad version` с ожидаемой.
- [ ] **I-L6** — `deployments/infra/nomad/nomad.hcl` — `bind_addr = "0.0.0.0"` для HTTP API. Защищён ACL, но при откате ACL bootstrap или misconfig API незащищён. Fix: `bind_addr = "127.0.0.1"` + advertise public_ip (уже есть), либо включить Nomad TLS.
- [ ] **I-L7** — `deployments/infra/nomad/{xauth,xhttp,xws}.nomad` — нет `service { check { ... } }`. Nomad считает аллокацию healthy сразу после старта процесса. Fix: HTTP-check на `/health` для xhttp (если открыть) или `script`-check (`nats pub --server ... api.v1.xX.ping`).
- [ ] **I-L8** — `deployments/envs/prod/setup.sh:128-131` — `apt-get install` без `--no-install-recommends`. Fix: добавить флаг.
- [ ] **I-L9** — `docs/production-setup.md:64-83` — таблица GitHub Secrets не содержит `ACCESS_SECRET` (xhttp), `COOKIE_SECURE`. `prod.md` содержит полный список → рассинхрон. Fix: синхронизировать.

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

**Напоминание:** x* — демо. Production-hardening не требуется. Но найденные баги влияют на демо-воспроизводимость → фиксятся.

### High

- [ ] **D-H1** — `cmd/xhttp/main.go:69` — `AccessSecret: []byte(os.Getenv("ACCESS_SECRET"))` без проверки на пустоту. Если переменная не задана — `VerifyJWT` подпишет пустым ключом и пропустит любой токен, подписанный пустым секретом. В `xauth` аналогичный код защищён `mustEnv`, в `xhttp` — нет. Fix: проверка через `mustEnv` / `log.Fatal` при пустом `ACCESS_SECRET`.
- [ ] **D-H2** — `internal/services/xws/manager.go:60-64` + `session.go:66` — data race на `session.timer`. `time.AfterFunc`-коллбэк читает/вызывает `sess.close()`, `resetTimer()` вызывает `timer.Reset(...)` из другой горутины. Без мьютекса. Fix: добавить `sync.Mutex` в `session`, брать его в `resetTimer()` и в начале коллбэка.
- [ ] **D-H3** — `internal/services/xws/manager.go:113-115` vs `manager.go:60-64` — одновременный disconnect-сообщение и истечение таймера → двойной `sess.close()` → двойной `Unsubscribe` + двойной CLOSE publish. Не фатально, но создаёт ошибки в логах. Fix: `sync.Once` в `session.close()`.
- [ ] **D-H4** — `internal/services/xauth/handlers.go:108-116` — `HandleRefresh` сначала делает `PutValue(oldJTI, "revoked")`, потом выдаёт новые токены через `issueTokenCookies`. Если выдача новых упадёт — старый JTI отозван, новых нет → клиент теряет сессию навсегда. Fix: поменять порядок — сначала выдать новые, потом отозвать старый.

### Medium

- [ ] **D-M1** — `internal/services/xhttp/cache.go:49` — `Invalidate` пишет пустой `[]byte{}` вместо `kv.Delete(key)`. Зависит от того, вернёт ли NATS SDK `entry.Value() == nil` или `[]byte{}` для такой записи. Fix: `kv.Delete(key)` (доступен с nats.go v1.x).
- [ ] **D-M2** — `internal/services/xauth/jwt.go:48-75` — `VerifyJWT` не проверяет `parts[0]` против ожидаемого header. Прямой вектор `alg:none` не работает (HMAC-compare пустой строки с expected вернёт false), но отсутствие явной проверки — code smell. Fix: `if parts[0] != jwtHeader { return Claims{}, errors.New("unexpected header") }`.
- [ ] **D-M3** — `internal/services/xws/manager.go:136-139` — `CloseAll` держит `Manager.mu` во время `nc.PublishMsg` и `Unsubscribe` каждой сессии. При медленном NATS блокирует весь менеджер. Fix: под локом — снимок списка сессий; закрытие — вне лока.
- [ ] **D-M4** — `internal/services/xhttp/handlers.go:147-174` — thundering herd на cache-miss. N параллельных запросов уходят в БД одновременно. Fix: `singleflight.Group`.

### Low

- [ ] **D-L1** — `internal/services/xhttp/handlers.go:56-77` — `HandleCreate` не валидирует пустой `name`. Fix: `if req.Name == "" { ReplyError(msg, 400, ...) }`.
- [ ] **D-L2** — `cmd/xws/main.go:54-66` — `sid` не валидируется. `.` или `*` создадут NATS-subject с wildcards. Fix: regex `^[a-zA-Z0-9_-]{8,64}$`.
- [ ] **D-L3** — `cmd/xhttp/main.go:69` + `internal/services/xhttp/config.go` — `ACCESS_SECRET` читается напрямую через `os.Getenv`, не через `Config`. Нарушение паттерна. Fix: добавить поле `AccessSecret []byte` в Config.

### Позитив

1. `hmac.Equal` в `HandleLogin` и `VerifyJWT` — timing-safe compare.
2. Refresh revocation через KV реализована семантически правильно (`GetValue` → `nil || "revoked"` трактуется как отозван).
3. Graceful shutdown: все сервисы выполняют `Drain`, xws дополнительно `CloseAll()` до Drain.
4. SQL полностью параметризован (`$1, $2, $3`) — инъекции исключены.
5. Изолированные KV-бакеты (`authms_refresh_tokens`, `xhttp_cache`) — нет коллизий ключей между сервисами.

---

## Глобальные рекомендации

Вне конкретных файлов — общая гигиена:

- [ ] **G1** — Нет CI-проверки на `go 1.26.1` → добавить `go vet`, `staticcheck`, `govulncheck` в `.github/workflows/ci.yml` отдельной джобой.
- [ ] **G2** — Нет `.golangci.yml` — включить базовый набор линтеров (errcheck, gosec, bodyclose, contextcheck, rowserrcheck).
- [ ] **G3** — Нет тестов нигде (`CLAUDE.md` явно говорит "No tests exist yet"). Минимум — table-tests на `utils/hosts.go`, `utils/env.go`, `ratelimit.go` (LRU логика); integration на gateway (HTTP→NATS) через `nats-test-server` или тестовый NATS в docker.
- [ ] **G4** — Нет X-Request-ID прогона в тестах → при рефакторинге recover.go регрессия не заметна. Fix: integration-тест который триггерит panic в NATS handler и проверяет что X-Request-Id из header попал в лог.
- [ ] **G5** — Нет метрик. zerolog-логи в stderr — достаточно для current стадии, но prometheus-эндпоинт на Gateway (request rate, WS connection count, NATS request latency) значительно упростит инцидент-расследование. Fix: добавить `expvar` или `prometheus/client_golang`, выставить на 127.0.0.1:9100.
- [ ] **G6** — Нет release notes / CHANGELOG. `build-N` pre-release'ы наполняются автоматом, но стабильные `v*` не имеют описания изменений.
- [ ] **G7** — Нет dependabot/renovate конфига. NATS SDK, zerolog, gorilla/websocket будут отставать. Fix: добавить `.github/dependabot.yml` с weekly check.

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
1. Присвоить ID по схеме `<область>-<приоритет><номер>`: области `P/I/D/G`, приоритеты `C/H/M/L`, номер — следующий свободный.
2. Формат: `` - [ ] **<ID>** — `file:line` — описание. Fix: решение.``

**Регрессии:**
Если закрытый пункт снова стал актуален — НЕ переоткрывать `[x]`. Создать новую запись `<ID>-regression` с описанием регрессии и датой.

**Полный ре-аудит:**
Раз в 2 недели (или после крупного рефакторинга): удалить закрытые пункты, обновить "Последний полный аудит" в шапке.
