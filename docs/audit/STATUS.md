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
- [x] **P-H5** — `internal/middleware/xauth.go:64` — проверка `Exp` access-токена без clock-skew tolerance. Исходная формулировка аудита про «двойную проверку `exp`» неверна: `xauth.VerifyJWT` намеренно **не** проверяет `Exp` (jwt.go:45-47 — by design, чтобы middleware мог разделять «expired» → /refresh и «invalid» → login). Реальный риск — при дрейфе часов между узлами (NTP ±секунды) свежевыданный токен может оказаться «истёкшим». Закрыт 2026-04-17: в `internal/middleware/xauth.go` добавлена константа `accessTokenClockSkew = 60 * time.Second`; проверка превратилась в `time.Now().Unix() > c.Exp + int64(skew.Seconds())`. RFC 7519 §4.1.4 рекомендует «small leeway»; 60s — индустриальный дефолт, жёстче смысла нет (access-токены короткоживущие).
- [x] **P-H6** — `internal/services/gateway/handlers.go:120,125` (аудитор указал 111/116 — ошибся номерами строк, тело верное) — `w.Write()` в `/health` молчаливо игнорировал ошибку. Закрыт 2026-04-17: оба вызова обёрнуты в `_, _ = w.Write(...)`, добавлен комментарий-пометка о намеренном игнорировании (после `WriteHeader` восстановиться нельзя; tcp-разрыв клиента в healthcheck — норма). Debug-лог не добавляли — Nomad бьёт `/health` каждые несколько секунд, любой transient разрыв создал бы шум в логах.

### Medium

- [x] **P-M1** — `internal/services/gateway/handlers.go:160` — маршрут `/v1/{service}/ws` не проверяет `r.Method`. WS upgrade принимает POST/PUT. Fix: `if r.Method != http.MethodGet { http.Error(...) }`. **2026-04-17:** в `route()` перед вызовом `handleWS` добавлена проверка `r.Method != http.MethodGet → 405`. Отбивает до `wsConnGuard`/`rand.Read`/`Upgrade` (без неё `gorilla` всё равно вернул бы 405, но уже после резерва WS-slot и генерации SID).
- [x] **P-M2** — `internal/services/gateway/handlers.go:186-188` — `service` и `methodParts` идут в NATS subject без санитизации. Символы `*`, `>`, пробел — спецсимволы NATS, позволяют манипулировать маршрутизацией. Fix: валидация через regex `^[a-zA-Z0-9_-]+$` для каждого сегмента. **2026-04-17:** добавлена пакетная переменная `validSubjectToken = regexp.MustCompile(`+"`"+`^[A-Za-z0-9_-]+$`+"`"+`)`; в `route()` цикл по `parts[1:]` (валидирует и service, и methodParts; покрывает обе ветки — HTTP и WS). Старый `if service == ""` удалён — пустая строка не проходит regex.
- [x] **P-M3** — `internal/services/gateway/ratelimit.go:184-197` — `realIP` возвращает `X-Real-IP` как есть, без `net.ParseIP`. Скомпрометированный или misconfigured прокси может передать пустую строку или мусор → все запросы учитываются под одним ключом, rate limit обходится. Fix: `if ip := net.ParseIP(v); ip != nil { return ip.String() }`. **2026-04-17:** в `realIP` (фактически строки 211-228, аудитор ошибся в номере) добавлен `net.ParseIP` поверх `X-Real-IP`; невалидное значение тихо пропускается, fallback на `r.RemoteAddr`. Защищает LRU rate-limiter от мусор-DoS при ротации невалидных X-Real-IP. `parsed.String()` нормализует IPv6.
- [x] **P-M4** — `internal/platform/logger/logger.go:31` — `zerolog.SetGlobalLevel` — глобальный side-effect в фабрике. Каждый вызов `logger.New()` перезаписывает уровень для всех логгеров процесса. Fix: выставлять уровень через `logger.Level(level)` на конкретном экземпляре. **2026-04-17:** `zerolog.SetGlobalLevel` удалён, уровень ставится через `.Level(level)` на возвращаемом instance. zerolog как библиотека не меняется. Default `GlobalLevel` остаётся на `DebugLevel(0)` → instance.Level — эффективный фильтр (zerolog `should()` берёт max(instance, global)).
- [x] **P-M5** — `internal/services/gateway/handlers.go:419-426` — нет `conn.SetReadLimit(...)`. gorilla defaults (32KB) легко насыщаются большими фреймами. Fix: `conn.SetReadLimit(64 * 1024)` после Upgrade. **2026-04-17:** добавлена константа `wsReadLimit = 64 * 1024` рядом с `wsWriteDeadline`/`wsReadDeadline`/`wsPingInterval`; `conn.SetReadLimit(wsReadLimit)` сразу после Upgrade. Аудитор ошибся в numerics — gorilla default `0` (unlimited), не 32 КБ; угроза верна (memory-DoS на одно соединение), фикс закрывает.
- [x] **P-M6** — `utils/log.go:8-13` — глобальная `pkgLog` без защиты (ни atomic, ни mutex). Сейчас `SetLogger` вызывается строго до старта горутин, но гарантии нет. Fix: `atomic.Pointer[zerolog.Logger]`. **2026-04-18:** выбран DI-вариант (compile-time гарантии вместо runtime accepted-risk). `utils/log.go` удалён вместе с `pkgLog`/`SetLogger`/`Logger`. `GetEnv`, `Reply`, `ReplyError`, `ParseAllowedHosts`, `AllowedHostSet.Allows` принимают `log zerolog.Logger` первым параметром. `LoadConfig(log)` во всех 4 сервисах; `cmd/*/main.go` пробрасывают `log` без `SetLogger`. Мёртвый код `MustParseAllowedHosts`/`AllowedHostsFromEnv` удалён. Гонки данных по логгеру невозможны: компилятор не позволит передать «не тот» экземпляр.
- [x] **P-M7** — `internal/services/gateway/handlers.go:444-465` — connect-сообщение отправляется через `Publish` (fire-and-forget) без подтверждения. При отсутствии подписчика WS остаётся открытым, не работая. Fix: использовать `RequestMsg` для connect с 2s таймаутом, при отказе — WS close 503. **2026-04-18:** `PublishMsg` заменён на `RequestMsgWithContext`; добавлено поле `HTTPConfig.WSConnectTimeout` + env `GATEWAY_WS_CONNECT_TIMEOUT` (default `2s`). При ошибке логируется Warn для `nats.ErrNoResponders` (понятная диагностика «сервис не подписан») и Error для остальных (DeadlineExceeded и т.п.); клиенту отправляется WS Close `1011 service unavailable` (CloseInternalServerErr — `1013 Try Again Later` в gorilla не предопределён, выбран ближайший стандартный код), defer закрывает соединение. Демо `cmd/xws/main.go` обновлён: после `mgr.Open(sid)` вызывает `msg.Respond(nil)` — ack-контракт стал частью протокола WS connect.
- [x] **P-M8** — `internal/services/gateway/handlers.go:514-550` — shutdown-горутина запускается без `sync.WaitGroup`. При return из `handleWS` горутина может обращаться к уже закрытому `conn`. Fix: `errgroup.Group` или `sync.WaitGroup`. **2026-04-18:** добавлена `var wg sync.WaitGroup` рядом с `mu`; обе побочные горутины (Ping и shutdown) обёрнуты в `wg.Add(1)`/`defer wg.Done()`. Объединённый defer теперь идёт по схеме `cancel() → wg.Wait() → mu.Lock() → conn.Close()` — гарантирует, что ни одна горутина не висит после возврата из `handleWS`. Дополнительно: в shutdown-горутине `conn.SetReadDeadline(time.Now())` шёл вне `mu` (формальная race с `conn.Close`); добавлен helper `safeSetReadDeadline` (по аналогии с `safeWrite`) — Lock + ctx.Done check + SetReadDeadline. Ping-горутина без race-окна вне mu, но включена в WaitGroup для симметрии lifecycle. errgroup отвергнут как ненужная абстракция — у горутин нет ошибок к возврату.

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
- [x] **I-C4** — `deployments/infra/nomad/{gateway,xauth,xhttp,xws}.nomad` — `artifact { checksum = var.CHECKSUM }` использует невалидный синтаксис: per Nomad docs `checksum` живёт внутри `options { checksum = "..." }`, не как direct attribute блока `artifact`. Найдено 2026-04-18 при `nomad job validate` в рамках I-M3. Все 4 файла отвергаются Nomad 1.11.x — то есть деплой через `nomad job run` падал на каждый запуск. Закрыт 2026-04-18: во всех 4 .nomad файлах `checksum = var.CHECKSUM` обёрнут в `options { checksum = var.CHECKSUM }`. Локальный `nomad job validate` (v1.11.3) после правки возвращает `Job validation successful` для всех 4 файлов с реальным sha256-vars. Поднят как Critical: блокирует deploy pipeline целиком.

### High

- [x] **I-H1** — `deployments/infra/nomad/nomad.hcl:32` + `deployments/envs/prod/setup.sh:177` — `bootstrap_expect = 1` на каждой ноде, риск split-brain при одновременном первичном bootstrap. Закрыт 2026-04-17 через документацию (без изменений конфига): в `prod.md` после раздела «Добавление новой ноды» добавлен callout «Важно: первичное развёртывание нескольких нод одновременно» с правилом sequential bootstrap (`nomad server members` + `raft list-peers` перед запуском следующей ноды); комментарии в `nomad.hcl` (строки 28-37) и `setup.sh` (строки 161-165) дополнены предупреждением и ссылкой на prod.md. `bootstrap_expect=3` не используем: для расширения существующего кластера он не подходит, а добавлять режим переключения через ENV — лишняя сложность под одноразовую операцию.
- [x] **I-H2** — `.github/workflows/{gateway,xauth,xhttp,xws}-deploy.yml` — 4 deploy-workflow триггерятся `workflow_run: CI` параллельно и делают `git pull` в одну `/opt/platform` → race на `.git/index.lock`. Закрыт 2026-04-17: тело SSH-команды в `.github/actions/deploy-nomad/action.yml` обёрнуто во `flock` (`exec 9>/var/lock/platform-deploy.lock; flock -w 300 -x 9`). Серилизует параллельные деплои + любые ручные SSH-деплои; `/var/lock` = `/run/lock` на Ubuntu (tmpfs 1777) → нет stale lock после reboot, прав root не нужно. Дескриптор 9 закрывается автоматически на выходе из шелла, lock освобождается тоже автоматически. Выбран серверный flock вместо GitHub `concurrency` — единая точка изменения, одинаково защищает CI- и manual-источники.
- [x] **I-H3** — `.github/actions/deploy-nomad/action.yml:85` — `nomad job run $VAR_ARGS` подвержен word-splitting. Закрыт 2026-04-17 как побочный эффект I-C1: переход на `NOMAD_VAR_*` env (через `printf %q` + `export`) устранил `$VAR_ARGS` целиком — теперь команда выглядит как `nomad job run /opt/platform/deployments/infra/nomad/${NOMAD_FILE}` без пользовательских значений в argv. Word-splitting невозможен по построению.
- [x] **I-H4** — `deployments/envs/prod/setup.sh:265-272` — `wget` тарбола NATS без SHA256/GPG верификации. Закрыт 2026-04-17 минимальным фиксом: `wget` заменён на `curl -fsSL` (универсальнее — присутствует в большинстве минимальных образов); после скачивания тарбола скрипт тянет `SHA256SUMS` из того же релиза, выбирает строку нужного файла через `awk` и верифицирует через `sha256sum -c`. Хардкод версионных хешей сознательно отвергнут как плохой паттерн (привязка ops-артефакта к коду). Защищает от passive errors (CDN bitrot, transient corruption); от активного MITM через github.com TLS не защищает — общий канал — но это вне реалистичного threat model для одноразового bootstrap-окна (~5-10s). HashiCorp GPG fingerprint check для Nomad сознательно не делали — формально вне scope I-H4, риск аналогичный, при необходимости можно добавить отдельным финдингом.
- [x] **I-H5** — `.github/workflows/setup.yml:76-79, 87` — `scp/ssh -o StrictHostKeyChecking=no` на новый IP, MITM получает `NATS_CA_KEY` (корень доверия NATS-кластера) + `NOMAD_TOKEN` (root-доступ к Nomad) + произвольное исполнение через поддельный SSH-эндпоинт. Закрыт 2026-04-17 гибридом: добавлен optional `host_fingerprint` input в `workflow_dispatch`; в шаге Configure SSH через `ssh-keyscan` забираются host keys целевой ноды и пишутся в `~/.ssh/known_hosts`; если `host_fingerprint` задан — `ssh-keygen -lf` сверяет с ним каждый ключ, иначе TOFU + WARN-лог fingerprint для ручной сверки оператором. В шагах Copy/Run заменено `StrictHostKeyChecking=no` → `=yes` — теперь неизвестный/изменённый ключ блокирует подключение. По умолчанию защита уже строже текущей (TOFU вместо «принять любой»); при заполненном fingerprint — полная MITM-защита первого подключения.
- [x] **I-H6** — `deployments/envs/prod/setup.sh:231-251` (nomad.service) — Nomad-агент от root, `raw_exec`-tasks наследуют root → каждая компрометация сервиса = root. Закрыт 2026-04-17 через per-task user (recommended pattern для raw_exec, не строго по тексту аудита): добавлено `user = "nomad"` в task-блоки `gateway.nomad`, `xauth.nomad`, `xhttp.nomad`, `xws.nomad` (4 файла). Системный user `nomad` уже создаётся в `setup.sh:155` и владеет `/var/lib/nomad`; `/etc/nomad/env` уже chmod 600 (root-only). Nomad-агент остаётся root по причине: упрощает alloc-dir creation, cgroups, log-rotation, минимизирует риск сломать Nomad management; raw_exec через setuid запускает каждую task'у как nomad. Шаблон в `prod.md` («Добавить новый сервис») обновлён — теперь содержит обязательный `user = "nomad"` с ссылкой на I-H6, чтобы новые сервисы не теряли non-root статус. Замечание: raw_exec не даёт namespace/cgroup isolation между tasks — это его фундаментальное ограничение, для настоящей изоляции нужен переход на `exec` или `docker` driver (отдельная архитектурная задача, не I-H6).

### Medium

- [x] **I-M1** — `.github/workflows/ci.yml:124-130` — `gh release list | tail -n +$(( KEEP + 1 )) | xargs gh release delete` без `jq 'sort_by(.createdAt)'`. Порядок по имени: `build-9 > build-10` лексикографически → удаляются не старейшие. Fix: `gh release list --json name,createdAt | jq 'sort_by(.createdAt) | reverse | .[KEEP:]'`. **2026-04-18:** `tail -n +$(( KEEP + 1 ))` удалён, jq переписан с явным `sort_by(.createdAt) | reverse | .[$keep:]` (через `--argjson keep "$KEEP"`). Уточнение к формулировке: текущий `gh release list` фактически отдаёт записи в date-desc через REST API (а не в лексикографическом порядке), но контракт CLI этого не гарантирует — при ручном rebuild старого тега `created_at` опередил бы tag-номер и `tail` снёс бы не самые старые билды. Новый pipeline проверен на синтетическом входе (`build-1..10`, неперемешанные даты) — корректно оставляет 5 свежих, удаляет остальные. Поле `createdAt` уже было в `--json` — лишних правок не потребовалось.
- [x] **I-M2** — `.github/actions/deploy-nomad/action.yml:67-126` (аудитор указал :47 — ошибся номером) — DNS-перебор нод выбирает первую доступную по SSH, но не проверяет статус Nomad leader. Нода в процессе перезагрузки имеет живой SSH, но мёртвый Nomad. Fix: `curl -sf http://$HOST:4646/v1/status/leader` перед `nomad job run`. **2026-04-18:** добавлен SSH-probe `curl -sf http://127.0.0.1:4646/v1/status/leader` сразу после определения ARCH; на не-2xx или connection error → `continue` на следующую ноду. Уточнение к рекомендации аудита: probe против `$HOST:4646` с runner'а не работает — порт 4646 публично закрыт между runner и нодами per CLAUDE.md (firewall разрешает только node-to-node), curl упал бы по connection-timeout вне зависимости от состояния Nomad. Probe через SSH к localhost:4646 обходит firewall и использует штатный анонимный endpoint (без NOMAD_TOKEN). Один SSH round-trip (~50-200ms) вместо ~30s nomad job run timeout per «зомби»-ноду.
- [x] **I-M3** — `deployments/infra/nomad/gateway.nomad:64` — `count = 1` + `max_parallel = 1` → окно недоступности 10+ секунд при rolling update. Fix: `count = 2` для gateway, либо явно задокументировать accepted downtime. **2026-04-18:** `type = "service"` → `type = "system"`, `count = 1` удалён. System job автоматически распределяет один alloc на каждую client-ноду — соответствует семантике DNS RR + static port `:8080` (раньше при count=1 N-1 нод обслуживали `connection refused`). Rolling update через `max_parallel=1` даёт окно недоступности только на обновляемой ноде, остальные продолжают принимать трафик. Локальный `nomad job validate` подтверждает корректность (после фикса I-C4 на parallel scope). Демо xservices не трогали — у них нет static port, count=1 для демо допустим.
- [x] **I-M4** — `deployments/envs/prod/setup.sh:317` (аудит указал :299 — ошибся номером) — `openssl genrsa 2048` с сертификатом на `days 3650`. NIST рекомендует RSA-3072+ или ECDSA P-256 для 10-летнего срока. Fix: `openssl ecparam -name prime256v1 -genkey -out node.key`. **2026-04-18:** заменён `openssl genrsa ... 2048` на `openssl ecparam -name prime256v1 -genkey -noout -out ...`. ECDSA P-256 — NIST-current, защита до 2050+ (RSA-2048 deprecated после 2030). `-noout` обязателен — иначе openssl выводит EC PARAMETERS блок до приватного ключа, что может сбить некоторые TLS-парсеры. Smoke-test через `openssl ec -in node.key -text -noout` подтверждает корректный 256-bit ключ. CA-key (приходит из env) не трогали — алгоритм node-key и CA-key независимы. CSR и x509-sign команды без изменений (openssl auto-detect формат key-файла; алгоритм подписи берётся из CA-key). `days 3650` оставлен — short-lived certs + auto-rotation отдельная архитектурная задача.
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

- [x] **D-H1** — `cmd/xhttp/main.go:69` — `AccessSecret: []byte(os.Getenv("ACCESS_SECRET"))` без проверки на пустоту → пустой HMAC-ключ принимает любой токен, подписанный таким же пустым. Закрыт 2026-04-17 минимальным inline-фиксом: до построения `authCfg` в `main.go` добавлен check `if accessSecret == "" { log.Fatal()... }` с подсказкой что значение должно совпадать с `AUTH_ACCESS_SECRET` xauth. Не выносили в `xhttp.LoadConfig` как в xauth — xhttp demo, минимизация изменений.
- [x] **D-H2** — `internal/services/xws/manager.go:60-64` + `session.go:66` — data race на `session.timer`. `time.AfterFunc`-коллбэк читает/вызывает `sess.close()`, `resetTimer()` вызывает `timer.Reset(...)` из другой горутины. Без мьютекса. Fix: добавить `sync.Mutex` в `session`, брать его в `resetTimer()` и в начале коллбэка. **2026-04-17:** в `session` добавлены `mu sync.Mutex` + `closed bool`; `resetTimer()` под мьютексом и no-op после close; `close()` под мьютексом стопит таймер. Закрыто совместно с D-H3 одной правкой `internal/services/xws/session.go`.
- [x] **D-H3** — `internal/services/xws/manager.go:113-115` vs `manager.go:60-64` — одновременный disconnect-сообщение и истечение таймера → двойной `sess.close()` → двойной `Unsubscribe` + двойной CLOSE publish. Не фатально, но создаёт ошибки в логах. Fix: `sync.Once` в `session.close()`. **2026-04-17:** вместо `sync.Once` — `closed bool` под `mu` (тот же мьютекс, что и для D-H2): `close()` идемпотентен, повторные вызовы — ранний return. Избыточные `sess.timer.Stop()` в `manager.go:113,137` оставлены (безопасны). Закрыто совместно с D-H2.
- [x] **D-H4** — `internal/services/xauth/handlers.go:108-116` — `HandleRefresh` сначала делает `PutValue(oldJTI, "revoked")`, потом выдаёт новые токены через `issueTokenCookies`. Если выдача новых упадёт — старый JTI отозван, новых нет → клиент теряет сессию навсегда. Fix: поменять порядок — сначала выдать новые, потом отозвать старый. **2026-04-17:** свопнут порядок — `issueTokenCookies` идёт первой; revoke старого JTI делается после, и его сбой только пишется в `Warn`-лог (не возвращает 500). Trade-off: сбой revoke оставляет старый JTI валидным до его `Exp` (replay window), но это сильно лучше потери сессии — новые куки клиент уже получил.

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
