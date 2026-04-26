# Аудит платформы — рабочий файл

**Последний полный аудит:** 2026-04-26
**Формат:** чекбокс `- [ ]` open · `- [x]` closed · `- [~]` deferred / won't fix.
**Соглашение:** при закрытии пункта ставить `[x]`, добавлять короткую пометку `(закрыт: <что сделано>)` и commit hash или дату. Файл — источник правды о состоянии платформы. Если находка закрыта, но вскрылся регресс — переоткрывать в новой секции "Регрессии".

**Закрытые пункты прошлых волн:** [`CLOSED-2026-04.md`](CLOSED-2026-04.md) — архив за период 2026-04-17 → 2026-04-25 (полный текст с описаниями фиксов и отвергнутыми альтернативами).

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

- [x] **P-M10** — `internal/services/gateway/handlers.go:236-237` (+ метки в `internal/platform/metrics/metrics.go:32,43,73,83`) — cardinality bomb на Prometheus-метрики. Метки `service`/`method` извлекаются из URL после `validSubjectToken`-валидации (`^[A-Za-z0-9_-]+$`), но это разрешает любую пару tokens, не только реальные backend-сервисы. Атакующий, посылающий 10⁴–10⁶ запросов на уникальные `/v1/{rand}/{rand}`, создаёт столько же label combinations в `gateway_http_requests_total` / `gateway_http_request_duration_seconds` / `nats_request_duration_seconds` / `nats_request_attempts_total`. NATS возвращает `ErrNoResponders` → 503, но `defer` уже инкрементировал метрику. Эффект: рост памяти client_golang (≈100 байт на timeseries × N), раздувание `/metrics`-выдачи (Prometheus federation захлёбывается), persistent ingestion bloat в downstream TSDB. Утверждение в closure-note G5 «кардинальность ограничена реальным набором бэкендов» неверно — реальный набор = всё, что прошло regex. Fix: один из вариантов — (а) аккумулировать неизвестные subject в фиксированный label `{service="unknown"}` (метрика записывается ТОЛЬКО при `err == nil` или error_type != ErrNoResponders), (б) задать allowlist через ENV `GATEWAY_METRICS_SERVICES=auth,http,ws` и отбрасывать всё вне списка в `unknown`, (в) использовать `prometheus.MaxAge`-обёртку с принудительным reset. Вариант (а) — минимально инвазивен, не требует ENV-конфига. (закрыт 2026-04-26: вариант (а), грануляция 1.A — обе метки сразу. В `handleHTTP` добавлен флаг `metricUnknown`, выставляется `true` при финальном `nats.ErrNoResponders` после исчерпания retry-цикла; defer для HTTP-метрик и блок NATS-метрик читают флаг и подставляют `service="unknown",method="unknown"` (для NATS — только service). Регрессивный риск «легитимный 503 на rolling deploy → unknown» принят: retry поглощает короткие окна, до метрики ErrNoResponders доходит только реально мёртвый subject. Doc-комментарии всех четырёх метрик в `metrics.go` обновлены — старая формулировка G5 («кардинальность ограничена реальным набором бэкендов») заменена на правдивую. Добавлен `TestIntegration_MetricsUnknownService` в `gateway_integration_test.go`: POST на `/v1/foo/bar` (нет подписчика) → проверяется delta=1 для `{service=unknown,method=unknown,status=503}` и delta=0 для `{service=foo,method=bar,status=503}` (HTTP), аналогично для NATSRequestAttemptsTotal. Прогон: `go build`, `go vet`, `go test -race`, `go test -race -tags=integration` — все зелёные.)

### Low

- [x] **P-L6** — `internal/services/gateway/handlers.go:122-135` — `handleHealth` принимает любой HTTP-метод. POST/PUT/DELETE на `/health` отвечают 200 с тем же JSON-телом. Не security-issue (тело не содержит секретов), но не идиоматично: HTTP healthcheck'и по конвенции — GET/HEAD; LB/мониторы могут принимать ответ POST как лишнюю мутацию. Fix: `if r.Method != http.MethodGet && r.Method != http.MethodHead { http.Error(w, "method not allowed", http.StatusMethodNotAllowed); return }` в начале. (закрыт 2026-04-26: проверка добавлена в начало `handleHealth`; не GET/HEAD → 405.)
- [x] **P-L7** — `internal/services/gateway/handlers.go:168-202` — путь `/v1/{service}` (без method-сегмента) проходит regex-валидацию, `parts[2:]` = `[]`, `strings.Join([], ".")` = `""`, итоговый subject = `api.v1.{service}.` (trailing dot — невалидный NATS subject). NATS возвращает `ErrNoResponders` → 503, в логах оператор видит confusing subject. Не security-issue, но плохой UX и засорение метрик (см. P-M10). Fix: после WS-ветки добавить `if len(parts) < 3 { http.Error(w, "method required", http.StatusBadRequest); return }`. (закрыт 2026-04-26: проверка `len(parts) < 3` после WS-ветки в `route`; `/v1/{service}` теперь отвечает 400 «method required» без обращения к NATS.)
- [x] **P-L8** — `internal/services/gateway/config.go:170-179` — `RateLimitConfig` не валидирует значения после загрузки. `GATEWAY_RATE_LIMIT_MAX_IPS=-1` → `len(table.m) >= -1` всегда false → eviction never triggers → unbounded growth (отменяет защиту P-H1). `GATEWAY_RATE_LIMIT=0` или `GATEWAY_RATE_BURST=0` → весь трафик отклоняется (`rate.Limit(0)` allow всегда false). `GATEWAY_MAX_WS_CONNS=-1` → `wsConns.Add(1) > -1` всегда true → все WS отклоняются. Misconfig оператора через ENV → silent degradation без логов. Fix: после `LoadConfig` — проверки `if cfg.RateLimit.MaxIPs <= 0 { return Config{}, fmt.Errorf("GATEWAY_RATE_LIMIT_MAX_IPS должен быть > 0") }` и аналогично для остальных полей; либо clamp к разумным дефолтам с Warn-логом. (закрыт 2026-04-26: добавлена `validateRateLimit` в `config.go`, вариант fail-fast — `Rate`/`Burst`/`MaxIPs`/`MaxWSConns` ≤0 возвращают error; `AuthRate`/`AuthBurst` проверяются только при непустом `AuthPathPrefix`. Оператор видит ошибку при старте, не молча через silent degradation.)
- [x] **P-L9** — `internal/middleware/recover.go:30` — `Bytes("stack", debug.Stack())` zerolog сериализует как base64. В JSON-логах stack trace выглядит как `"stack":"Z29yb3V0aW5lIDQz..."` — нечитаемо без декодирования, теряется ценность для debug панику в проде. Fix: `Str("stack", string(debug.Stack()))` — JSON-escape сохранит читабельность (`\n` → `\\n`, всё разбираемо `jq`). (закрыт 2026-04-26: `Bytes` → `Str(string(debug.Stack()))`; в логах stack trace теперь JSON-escaped, читабелен через `jq -r '.stack'`.)
- [x] **P-L10** — `internal/services/gateway/handlers.go:266-268` — `r.Header.Get("Cookie")` возвращает только первое значение заголовка `Cookie`. RFC 6265 §5.4 не запрещает множественные `Cookie:`-заголовки (в HTTP/2 вообще нормирует на один, но HTTP/1.1 от прокси/middlebox может прийти несколько). При наличии 2+ заголовков теряются последующие → у backend нет всех кук → невалидная авторизация. Fix: `cookies := strings.Join(r.Header.Values("Cookie"), "; ")`. Аналогично в WS-ветке `handlers.go:539-541`. (закрыт 2026-04-26: оба места — HTTP-handler и WS-connect — переведены на `strings.Join(r.Header.Values("Cookie"), "; ")`. Все Cookie-заголовки теперь сшиваются в одну строку через `; ` перед прокидыванием в NATS.)
- [x] **P-L11** — `internal/services/gateway/gateway_integration_test.go:343-376` — `TestIntegration_HealthEndpoint` проверяет только 200-ветку (NATS подключён). Doc-комментарий теста явно обещает: «возвращает 200 при подключённом NATS **и 503 при разрыве соединения**», но 503-кейс не реализован — нет вызова `conn.Close()` + повторного запроса. Регрессия в `handleHealth` (например, инверсия `gw.nats.Conn.IsConnected()` или удаление 503-ветки) тестом не ловится — Nomad-self-healing молча перестанет работать, deploy продолжит крутить «healthy» без подсветки проблемы. Fix: добавить вторую часть теста — `conn.Close()`, ждать `IsConnected() == false` (возможно через polling до 1s), новый GET `/health` → проверить `StatusCode == 503` и body `{"status":"error","nats":"disconnected"}`. Нюанс: после `conn.Close()` `nc.PlatformClient.Conn` указывает на тот же закрытый conn — `IsConnected()` корректно вернёт false. Severity Low: тест-coverage gap, не баг в продакшен-коде. (закрыт 2026-04-26: после первого Get'а в тесте — `conn.Close()`, polling до 1s по `conn.IsConnected()`, новый GET `/health` → проверка StatusCode=503 + body точное совпадение с `{"status":"error","nats":"disconnected"}`. Регрессия в `handleHealth` теперь падает на этой ветке. `go test -race -tags=integration` зелёный.)

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

- [x] **I-H7** — `.github/actions/deploy-nomad/action.yml:44-49` — SSH-команда использует `-o StrictHostKeyChecking=no`. По I-H5 (закрыт 2026-04-17) `setup.yml` уже мигрирован на гибрид «`ssh-keyscan` + optional `host_fingerprint` input + `StrictHostKeyChecking=yes`», но deploy-action остался на «принять любой ключ». Запускается на **каждом push в main** — гораздо чаще, чем setup.yml (один раз на новую ноду). Атакующий, способный делать MITM между GitHub Actions runner (Azure DC) и prod-нодой, перехватывает: (1) `NOMAD_TOKEN` (root-доступ к Nomad-кластеру через ACL), (2) все `NOMAD_VAR_*`-секреты (`NATS_PASSWORD`, `AUTH_ACCESS_SECRET`, `AUTH_REFRESH_SECRET`, `DATABASE_URL`, `ALLOWED_HOSTS`, `GATEWAY_TRUSTED_PROXY`), (3) deploy-команду (можно подменить на запуск произвольного `nomad job run` с другим artifact). Поднят как High по impact (полная компрометация всех секретов + RCE на Nomad-кластере); probability ниже среднего (требуется network-level доступ на маршруте), но симметрия с I-H5 диктует одинаковый уровень защиты. Fix: симметрично с I-H5 — добавить `host_fingerprints` (множественное число — 1+ нод в DNS) input (или GitHub Secret), на runner'е через `ssh-keyscan` забирать host keys всех нод из `dig +short PLATFORM_DOMAIN A`, при заполненном fingerprint — verify через `ssh-keygen -lf`, `StrictHostKeyChecking=yes` в SSH-команде. Альтернатива — хранить готовый `known_hosts` в репо (синхронизированный с реальным состоянием кластера), но при ротации host-ключей он устаревает. (закрыт 2026-04-26: в `action.yml` после `dig` добавлен ssh-keyscan по всем хостам в `mktemp`-файл, при заполненном `HOST_FINGERPRINTS` — multi-host сверка через `ssh-keygen -lf` + grep по строке хоста, поддержка multi-line/comma-separated формата + `tr -d '\r'` против CRLF из Windows-источника; нерезолвящаяся нода (пустой ответ ssh-keyscan) исключается из деплоя без fail — сохраняет старую availability «один down → деплой через другой»; несовпадение fingerprint'а = потенциальный MITM → fail run целиком + печать полученных fps для диагностики; пустой секрет → TOFU + WARN. SSH перешёл на `StrictHostKeyChecking=yes` + `UserKnownHostsFile=$KNOWN_HOSTS_FILE`; в 4 `*-deploy.yml` секрет `HOST_FINGERPRINTS` добавлен в env-блок job. Оператору на проде: после мерджа задать GitHub Secret `HOST_FINGERPRINTS` (по одному SHA256:... на строку или через запятую), иначе пайплайн идёт в TOFU-режиме.)

### Medium

- [x] **I-M11** — `deployments/infra/nats/nats.conf:17` (+ дублирующий HEREDOC `deployments/envs/prod/setup.sh:413`) — `http_port: 8222` слушает на `0.0.0.0:8222` (default bind, без явного `host`). Защита — только UFW (`setup_firewall:511-521` НЕ открывает 8222 наружу), но это single point of failure: `ufw disable` для отладки, ручное `ufw allow from any` оператором, отказ ufw-юнита, или провайдер с собственным firewall впереди VPS — public leak `/varz` (server config), `/connz` (список клиентов с IP), `/jsz` (JetStream stream details), `/healthz`. Defense-in-depth требует loopback-bind как первичного слоя, firewall — вторичного (та же логика, что в I-L6 для Nomad HTTP API). Дополнительно: `setup.sh:595` логирует «NATS мониторинг: http://${NODE_IP}:8222» — при текущем UFW-состоянии оператор не сможет туда зайти, нужно через SSH-tunnel. Fix: в `nats.conf` (и в HEREDOC `setup.sh:411-452`) заменить `http_port: 8222` на `http: "127.0.0.1:8222"` (это валидный NATS-синтаксис, переопределяет адрес monitoring-listener'а; `monitor`-блока в NATS нет — только top-level `host:` или `http:`); строку 595 setup.sh заменить на «NATS мониторинг: SSH-tunnel `ssh -L 8222:127.0.0.1:8222 user@${NODE_IP}` → http://localhost:8222». Затрагивает обе HEREDOC-копии (см. memory `feedback_setup_heredoc_drift`). (закрыт 2026-04-26: `nats.conf` и HEREDOC-копия в `setup.sh` синхронно переведены на `http: "127.0.0.1:8222"`; комментарий заменён на «Мониторинг — loopback-only (внешний доступ через SSH-tunnel)» с пояснением defense-in-depth; комментарий-якорь в `setup.sh:539-540` обновлён с «http_port» на «`http:`»; print_summary логирует SSH-tunnel-инструкцию симметрично с Nomad UI на следующей строке. Wait-loop `curl 127.0.0.1:8222/healthz` уже использует loopback — после фикса работает без изменений. Прогон: `bash -n setup.sh` чисто; рантайм-проверка на проде после мерджа — `curl http://127.0.0.1:8222/healthz` отвечает изнутри ноды, `curl http://${NODE_IP}:8222/healthz` снаружи — соединение отвергается на kernel-уровне (loopback-only listener). prod.md упоминаний 8222 не содержит.)

### Low

- [x] **I-L10** — `deployments/envs/dev/start.sh:172` — `for svc in $(ls "$ROOT_DIR/cmd/")`. Тот же паттерн, что был исправлен в `ci.yml` (I-L4, 2026-04-18) и `release.yml` (I-L4-regression, 2026-04-25) на bash glob с `nullglob`. Локальная сборка ломается, как только в `cmd/` появляется файл (`README`, `.gitkeep`, `.DS_Store`) — `go build -o bin/.DS_Store ./cmd/.DS_Store` выдаёт «package not found» и `set -e` валит весь скрипт. Третья регрессия одного класса в одном репо. Fix: идентично `ci.yml`/`release.yml` — `shopt -s nullglob; for dir in cmd/*/; do svc="${dir%/}"; svc="${svc#cmd/}"; ... done`. (закрыт 2026-04-26: `build_binaries` в start.sh переведён на `shopt -s nullglob; for dir in cmd/*/`, идентично с ci.yml/release.yml. `bash -n` чисто.)
- [x] **I-L11** — `deployments/envs/prod/setup.sh:154-155` — `wget -qO /usr/share/keyrings/hashicorp-archive-keyring.gpg https://apt.releases.hashicorp.com/gpg`. GPG-ключ скачивается через TLS, но fingerprint не верифицируется. При компрометации HashiCorp APT-mirror'а (или конкретно этого endpoint'а) на ноду ставится backdoored `nomad`-binary, который запускается с root-привилегиями (raw_exec → tasks под `user=nomad`, но Nomad-агент сам root в systemd-юните `setup.sh:265`). Тот же класс уязвимости, что закрыт в I-H4 для NATS-тарбола (там введена SHA256SUMS-сверка); HashiCorp использует GPG с фиксированным fingerprint'ом `798AEC654E5C15428C8E42EEAA16FCBCA621E701` (опубликован на их сайте). Fix: после `wget` — `actual=$(gpg --show-keys --with-fingerprint /usr/share/keyrings/hashicorp-archive-keyring.gpg | awk '/Key fingerprint/ {gsub(/ /,"",$0); print substr($0, length($0)-39); exit}')` (или `gpg --import-options show-only --import` + `awk`); сравнить с hardcoded `EXPECTED=798AEC654E5C15428C8E42EEAA16FCBCA621E701`, при несовпадении `die`. Severity Low: атака требует компрометации HashiCorp-инфры или маршрута до `apt.releases.hashicorp.com`. (закрыт 2026-04-26: после `wget` GPG-ключа добавлена сверка fingerprint'а через `gpg --show-keys --with-colons` + `awk -F: '/^fpr:/ {print $10; exit}'`; expected=`798AEC654E5C15428C8E42EEAA16FCBCA621E701`, при несовпадении — `die`. `bash -n` чисто.)
- [x] **I-L12** — `deployments/envs/prod/setup.sh:517-518` — `ufw allow 4646/tcp comment 'Nomad HTTP API'`. После I-L6 (закрыт 2026-04-19) Nomad HTTP API слушает только на `127.0.0.1:4646` (через `addresses { http = "127.0.0.1" }`). Внешний пакет на `<NODE_IP>:4646` отбрасывается на kernel-уровне (loopback-only listener), независимо от UFW. Правило — dead code и одновременно скрытый risk: при будущем рефакторинге, если кто-то уберёт `addresses{}` из `nomad.hcl` (например, для диагностики и забудет вернуть), UFW не заметит и сразу пустит трафик. Fix: удалить строку 517 (либо обе — 517 и 518; 4646 публичный listener-сценарий не нужен ни сейчас, ни планово). Сверка с I-L6 closure: «при откате ACL bootstrap или misconfig порт 4646 наружу не открывается даже временно» — UFW-allow подрывает это утверждение. (закрыт 2026-04-26: строка `ufw allow 4646/tcp` удалена; комментарий-блок обновлён — явно указано, что Nomad HTTP API на loopback и UFW-правило было бы dead-code. При откате `addresses{}` в `nomad.hcl` UFW теперь не пустит 4646 наружу автоматически.)
- [x] **I-L13** — `deployments/envs/prod/prod.md` рассинхронизирован с волной фиксов 2026-04-18..2026-04-25 в трёх точках: (a) **строка 168** — «CI автоматически подхватит его при следующем push — `for svc in $(ls cmd/)`». Этот паттерн закрыт в I-L4 (2026-04-18) для `ci.yml` и в I-L4-regression (2026-04-25) для `release.yml`; реальный CI использует `shopt -s nullglob; for dir in cmd/*/`. Контрибьютор, скопировавший «for svc in $(ls cmd/)» в свой service-template, получит сломанный билд при появлении файла в `cmd/`. (b) **строки 451-455** — «При первом деплое новой архитектуры: `nomad job stop platform`, `nomad job stop xservices`». Эта инструкция актуальна только для миграции с устаревшей структуры (один `platform.nomad` + один `xservices.nomad`), которая прошла до 2026-04-18 (по I-M7 prod.vars.example уже содержит 4 отдельных команды для gateway/xauth/xhttp/xws). Для новых установок раздел нерелевантен и сбивает с толку («что за `platform`-job?»). (c) **строка 528** — Firewall-таблица содержит `4646 TCP — Nomad HTTP API` в разделе «межнодовые порты». После I-L6 Nomad HTTP API слушает только на 127.0.0.1, между нодами нужны только 4647/4648 (RPC/Serf). Указание оператору открывать 4646 между нодами — unused, и пересекается с дублированием в I-L12 на UFW-уровне. Fix: (a) заменить «`for svc in $(ls cmd/)`» на «`shopt -s nullglob; for dir in cmd/*/`»; (b) убрать или явно пометить «Legacy (только при миграции с до-2026-04-18 архитектуры; для новых установок не нужно)»; (c) убрать строку 4646 из firewall-таблицы или дописать примечание «(только loopback на ноде; между нодами не нужен)». Severity Low: документация, не код. (закрыт 2026-04-26: (a) строка 168 переведена на канонический `shopt -s nullglob; for dir in cmd/*/`; (b) блок про `nomad job stop platform/xservices` обёрнут в blockquote-warning «Legacy (только для миграции с до-2026-04-18 архитектуры)» с прямым указанием, что для свежих установок шаг не нужен; (c) строка 4646 удалена из firewall-таблицы, добавлено примечание о loopback-listener'ах для 4646 и 8222 со ссылкой на SSH-tunnel.)

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

- [x] **D-L4** — `internal/services/xhttp/handlers.go:185-217` — `HandleUpdate` не валидирует пустой `req.Name`. По симметрии с D-L1 (закрыт 2026-04-19, добавлена аналогичная проверка в `HandleCreate`) `name=""` через UPDATE сохраняется в БД без ошибки. Closure-note D-L1 явно фиксирует follow-up: «`HandleUpdate` не трогали — отдельная находка не открыта, при необходимости откроется при следующем audit'е». Fix: после `json.Unmarshal` в HandleUpdate — `if req.Name == "" { utils.ReplyError(h.log, msg, 400, "name required"); return }`. Цена — 4 строки в demo, без новых платформенных dep — допустимо. (закрыт 2026-04-26: проверка добавлена после `json.Unmarshal` в `HandleUpdate` симметрично с `HandleCreate`. `go build` / `go vet` / `go test -race` зелёные.)

### Позитив

1. `hmac.Equal` в `HandleLogin` и `VerifyJWT` — timing-safe compare.
2. Refresh revocation через KV реализована семантически правильно (`GetValue` → `nil || "revoked"` трактуется как отозван).
3. Graceful shutdown: все сервисы выполняют `Drain`, xws дополнительно `CloseAll()` до Drain.
4. SQL полностью параметризован (`$1, $2, $3`) — инъекции исключены.
5. Изолированные KV-бакеты (`authms_refresh_tokens`, `xhttp_cache`) — нет коллизий ключей между сервисами.

---

## Frontend

Код: `xfrontend/` — React + Vite клиент для ручного тестирования демо-сервисов (xauth/xhttp/xws). По соглашению префикса `x*` — демо, замещается в реальных проектах. Платформенный scope — только в части (а) корректного протокола взаимодействия с Gateway (cookies, WS handshake) и (б) синхронизации с backend-API. Production-hardening UI/UX, доступность, тесты — вне scope.

### Critical

_(пусто)_

### High

_(пусто)_

### Medium

_(пусто)_

### Low

- [~] **F-L1** — `xfrontend/src/tabs/Auth.tsx:5-6` — `useState('admin')` / `useState('dev-password')` хардкодит дефолтные credentials в исходник. После `vite build` эти значения попадают в `dist/assets/index-*.js` (минифицированный bundle), и любой, кто откроет собранный фронт в браузере, увидит их в текстовом виде. Для dev-окружения это удобно (быстрый тест), для prod-deploy xfrontend с теми же значениями — leak default credentials. xfrontend официально demo, так что это в зоне accept-risk, но по симметрии с D-H1 (закрыт 2026-04-17, fail-fast при пустом ACCESS_SECRET в xhttp) frontend тоже стоит привести к «дефолтные пустые, оператор вводит сам». Fix: `useState('')` для обоих полей, добавить placeholder/hint в README.md `xfrontend/`-раздел про admin/dev-password как известные dev-default'ы. Cost: 2 строки в Auth.tsx + комментарий в README. Severity Low: демо, dev-окружение. (wontfix 2026-04-26: первоначально закрыт пустыми `useState`, но откачен — xfrontend это demo для ручного теста, хардкод `admin`/`dev-password` нужен ровно для UX «нажал login и оно работает»; production-проект xfrontend полностью заменяет своим фронтом, поэтому accept-risk остаётся валидным даже при `vite build` → `dist/`. Описание в этом пункте само говорит «accept-risk» — Fix-блок противоречил описанию, не следовало предлагать.)
- [x] **F-L2** — `xfrontend/src/api.ts:12-41` — `apiCall` использует `fetch` без таймаута / `AbortController`. Если backend завис (NATS-disconnect, retry-цикл Gateway > GATEWAY_NATS_REQUEST_TIMEOUT 5s + WS_CONNECT_TIMEOUT 2s), `fetch` ждёт по умолчанию ≥30 секунд (зависит от браузера) — кнопка остаётся `disabled`, `busy=true` не сбрасывается, пользователь демо-фронта не понимает что произошло. Не security-issue (UX gap), но симптоматика «всё зависло» тяжелее интерпретируется чем «timeout: backend не отвечает». Fix: добавить общий таймаут в `apiCall` через `AbortSignal.timeout(8000)` (обёртка в стандарте, без полифилла нужен только TS lib `DOM` — он уже включён в tsconfig); при `AbortError` возвращать `{ ok: false, status: 0, error: 'request timeout' }`. Cost: 4 строки. Severity Low: UX демо, не баг. (закрыт 2026-04-26: `apiCall` обёрнут в try/catch вокруг `fetch`; `signal: AbortSignal.timeout(8000)` вставлен после spread'а `init` (перекрывает любой пользовательский signal); `DOMException name === 'TimeoutError'` → `{ok: false, status: 0, error: 'request timeout'}`. `tsc --noEmit` чисто.)

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

- [x] **G9** — `README.md` рассинхронизирован с фактической структурой репозитория после волны фиксов 2026-04-25. Конкретно: (a) раздел «Структура каталогов» (строки 162-178) НЕ упоминает `internal/platform/metrics/` — пакет, добавленный в G5 (Prometheus-метрики Gateway); (b) нет раздела «Метрики» / «Наблюдаемость» — CLAUDE.md его содержит (раздел Metrics с описанием endpoint'а и набора метрик), README — нет, что нарушает паттерн «README — entry-point документации» (зафиксированный в G8 closure-note). Fix: (1) в дерево каталогов между `internal/platform/nc/` и `internal/platform/logger/` добавить `metrics/` с подписью «Prometheus-метрики (gateway HTTP, NATS, WS, rate limiter)»; (2) после раздела «Распределение ресурсов» добавить раздел «Метрики и наблюдаемость» с описанием `/metrics` endpoint'а и ссылкой на CLAUDE.md → Metrics; (3) опционально — после G8-стиля добавить ссылку «Подробная конфигурация — `CLAUDE.md` → Metrics» для уменьшения дублирования. Минимальный fix — два изменения, синхронизация без полного рефакторинга README. (закрыт 2026-04-26: (1) в дереве каталогов между `nc/` и `logger/` добавлен `metrics/` с описанием; (2) после раздела «Распределение ресурсов» добавлен раздел «Метрики и наблюдаемость» — описан loopback-bind 127.0.0.1:8081, перечислены ключевые метрики (HTTP, NATS, WS, rate limiter, freebies от client_golang), доступ через SSH-tunnel и Prometheus federation, ссылка на CLAUDE.md → Metrics для подробностей.)

---

## Регрессии

Регрессии открываются при возврате ранее закрытого пункта в неверное состояние. ID — `<original>-regression`. Закрытие — пометить `[x]` с датой и кратким описанием фикса.

_(пусто)_

---

## Контекст ре-аудита 2026-04-26

Метаданные текущей волны находок. После закрытия всех `[ ]`-пунктов выше эта секция архивируется вместе с ними в `CLOSED-YYYY-MM.md`.

**Прочитанные файлы (источник находок):**
- Platform: `cmd/gateway/main.go`, `internal/platform/{nc/{client,health}.go,logger/logger.go,metrics/metrics.go}`, `internal/middleware/{recover,xauth,recover_test}.go`, `internal/services/gateway/{config,handlers,ratelimit,gateway_integration_test,ratelimit_test}.go`, `utils/{cookie,env,hosts,reply,env_test,hosts_test}.go`
- Infra/CI: `.github/workflows/{ci,release,setup,gateway-deploy}.yml`, `.github/actions/deploy-nomad/action.yml`, `.github/{dependabot,release}.yml`, `.golangci.yml`, `deployments/infra/nomad/{nomad.hcl,gateway,xauth,xhttp,xws}.nomad`, `deployments/infra/nats/nats.conf`, `deployments/envs/prod/{setup.sh,prod.md,prod.vars.example}`, `deployments/envs/dev/{start.sh,docker-compose.yml,dev.md,nats.conf,dev.vars}`
- Demo (Go): `cmd/{xauth,xhttp,xws}/main.go`, `internal/services/{xauth/{config,handlers,jwt}.go,xhttp/{cache,config,handlers,migrate}.go,xws/{config,manager,session}.go}`
- Frontend (xfrontend): `package.json`, `package-lock.json` (просмотрено), `vite.config.ts`, `tsconfig.json`, `index.html`, `.env.example`, `.gitignore`, `src/{main,App,api}.{tsx,ts}`, `src/tabs/{Auth,Crud,Ws}.tsx`
- Global: `go.mod`, `README.md`, главный `.gitignore`

Расширения scope в этой волне:
- Этап 1 (первичный проход) — основной платформенный код + Infra/CI + ключевые демо-файлы; дал 13 находок (P-M10, P-L6..L10, I-H7, I-M11, I-L10..L12, D-L4, G9).
- Этап 2 (расширенный аудит по запросу) — prod.md (даёт I-L13), gateway_integration_test.go (даёт P-L11), recover_test.go/ratelimit_test.go/env_test.go/hosts_test.go (тесты чисты), dev/{docker-compose.yml,dev.md,nats.conf,dev.vars} (чисто).
- Этап 3 (закрытие demo scope) — xauth/{config,jwt}.go, xhttp/{config,migrate}.go, xws/config.go, cmd/xhttp/main.go (без новых находок: D-H1/D-L3/D-M5/D-M2 закрытия в архиве адресовали ключевые проблемы этих файлов; новых классов не появилось).
- Этап 4 (frontend scope) — xfrontend/ полностью; даёт F-L1 (hardcoded credentials в Auth.tsx), F-L2 (apiCall без timeout). dist/ и node_modules/ не в git (xfrontend/.gitignore покрывает); package-lock.json присутствует — supply-chain reproducibility ОК. Атак-поверхность frontend минимальная: нет dangerous innerHTML, нет eval, fetch с credentials:include через same-origin proxy.

**Намеренно не прочитано** (accept-risk scope cuts):
- _(весь scope этой волны прочитан; список пуст)_

**Severity rationale (подтверждено advisor 2026-04-26):**
- **P-M10 Medium** (не High) — per-timeseries ~100 байт; gigabyte-scale memory leak в client_golang требует миллионов уникальных path-комбинаций, что отсекается rate-limiter'ом (per-IP burst=200, default rate=100 r/s). Реальная жертва — downstream Prometheus federation / TSDB ingestion bloat. Атака возможна, но не катастрофична.
- **I-H7 High** — симметрично с I-H5: те же секреты (`NOMAD_TOKEN`, все `NOMAD_VAR_*`) в SSH-сессии, тот же MITM-вектор; ключевое отличие — запускается на **каждый push в main**, а не один раз на новую ноду. Несимметричность защиты (`=yes` в setup, `=no` в deploy) — gap, который сложно обосновать. High по impact, симметрия с I-H5 диктует уровень.
- **I-M11 Medium** (не High) — единственный текущий слой защиты — UFW; loopback-bind = defense-in-depth. Не leak секретов сам по себе (только метаданные через `/varz`/`/connz`), но при `ufw disable` для отладки или ошибке оператора — public leak. Та же логика, что в I-L6 для Nomad HTTP API.

**Что НЕ нашлось** (явное отсутствие = подтверждение правильности):
- Critical в Platform: 0 — `cmd/gateway/main.go` lifecycle и shutdown-последовательность чисты после волны 2026-04-17.
- High в Platform: 0 — после P-H1...P-H6 (закрыты) и P-M9 (built-in health) типовые race / busy-wait / fatal-в-горутине устранены.
- Critical/High в Demo: 0 — D-H1...D-H4 (закрыты) убрали ключевые баги в xauth/xws; новых классов проблем не появилось.
- Critical в Infra: 0 — I-C1...I-C4 (закрыты) убрали все блокирующие deploy-баги; новых вводов в pipeline не было.

**Стиль работы (методические заметки для будущих ре-аудитов):**
- Архивирование закрытых пунктов в `CLOSED-YYYY-MM.md` оставляет STATUS.md чистым — стало понятно с первого взгляда, какие активные находки есть.
- Нумерация ID идёт **сквозная** через архив: P-M10 после архивных P-M1...P-M9. Это позволяет ссылаться на любой исторический пункт без коллизий и видеть «номер» как метку «когда это нашлось».
- Findings с Fix-вариантами (а/б/в) лучше единственного варианта — позволяет обсудить trade-offs до реализации.
- Advisor вызывался один раз перед записью — поймал две правки (false-alarm в G9 и невалидный NATS-syntax в I-M11). Дешевле, чем catch на этапе реализации.

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
