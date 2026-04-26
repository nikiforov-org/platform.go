# Production-деплой

## Архитектура

Каждая нода запускает:
- **NATS** — часть кластера, связанного через DNS Discovery (`PLATFORM_DOMAIN`)
- **Nomad** — hybrid server+client, управляет сервисами через raw_exec
- **Go-бинарники** — запускаются Nomad напрямую, без Docker

Трафик: DNS → Gateway (:8080) → NATS → сервисы.

---

## Добавление новой ноды — 3 шага

### 1. Купить VPS

Ubuntu 22.04 или 24.04. SSH-ключ из GitHub Secret `DEPLOY_SSH_KEY` должен быть
добавлен в `~/.ssh/authorized_keys` при создании (через панель провайдера).

### 2. Добавить A-запись DNS

Добавьте A-запись вашего кластерного домена (`PLATFORM_DOMAIN`) → IP новой ноды.

| Тип | Имя                   | Значение    |
|-----|-----------------------|-------------|
| A   | `nodes.example.com`   | `IP_НОДЫ`   |

Одновременно можно добавить A-запись публичного домена (для балансировки — см. раздел ниже).

### 3. Запустить одну команду на сервере

```bash
wget -qO- https://raw.githubusercontent.com/OWNER/REPO/main/deployments/envs/prod/setup.sh \
  | PLATFORM_DOMAIN=nodes.example.com \
    NATS_USER=nats \
    NATS_PASSWORD=secret \
    bash
```

Или через curl:
```bash
curl -fsSL https://raw.githubusercontent.com/OWNER/REPO/main/deployments/envs/prod/setup.sh \
  | PLATFORM_DOMAIN=nodes.example.com \
    NATS_USER=nats \
    NATS_PASSWORD=secret \
    bash
```

Скрипт (~2-3 мин):
- Настраивает оптимальный swap (2×RAM, ≤10% диска, ≤4 GB)
- Устанавливает Nomad и NATS
- Создаёт systemd-сервисы с credentials в `/etc/nats/env`, `/etc/nomad/env` (chmod 600)
- Настраивает ufw
- Запускает сервисы

**После запуска скрипта нода автоматически находит остальные ноды через DNS
и входит в кластер — неважно, первая ли это нода или двадцать первая.**

> **Важно: первичное развёртывание нескольких нод одновременно**
>
> `bootstrap_expect = 1` означает, что каждая нода стартует сразу готовой
> как single-node Raft-лидер. При расширении уже работающего кластера это
> удобно: новая нода найдёт старые через DNS и присоединится как follower.
>
> Но если поднимать **2+ новых нод параллельно** до первого bootstrap кластера —
> возникает окно риска: пока DNS-записи ещё не пропагировались или ноды не
> видят друг друга по сети, каждая забутстрапится как самостоятельный лидер.
> Autopilot позже сольёт их через Raft, но часть Job state, записанного в этом
> окне, может быть потеряна.
>
> **Правило при создании кластера с нуля:** ноды поднимать **последовательно**.
> После запуска `setup.sh` на первой ноде дождаться:
>
> ```bash
> nomad server members      # Status=alive
> nomad operator raft list-peers   # должен быть один peer, Voter=true
> ```
>
> Только после этого запускать `setup.sh` на следующей ноде. Когда кластер
> уже работает — порядок добавления не важен.

---

## Балансировка трафика

Доступно два варианта — они не исключают друг друга.

### Вариант 1: Пассивная балансировка через DNS (round-robin)

Добавьте A-записи публичного домена для каждой ноды с Gateway:

| Тип | Имя               | Значение  |
|-----|-------------------|-----------|
| A   | `api.example.com` | `IP_1`    |
| A   | `api.example.com` | `IP_2`    |
| A   | `api.example.com` | `IP_3`    |

DNS-клиенты случайно выбирают один из IP. Без единой точки отказа, бесплатно.
`GATEWAY_TRUSTED_PROXY` не нужен.

### Вариант 2: Балансировщик нагрузки (managed LB)

Один A-запись публичного домена → IP балансировщика:

| Тип | Имя               | Значение  |
|-----|-------------------|-----------|
| A   | `api.example.com` | `LB_IP`   |

LB проксирует трафик на все ноды (:8080) и проставляет `X-Real-IP`.
Задайте `GATEWAY_TRUSTED_PROXY = IP_LB` в GitHub Secrets — Gateway будет
доверять `X-Real-IP` только от LB, защищая rate limiter от спуфинга.

---

## CI/CD (GitHub Actions)

### Схема

```
push → main
  └── CI (ci.yml)
        ├── Build & Test
        └── Release
              ├── Сборка бинарников: авто-дискавери из cmd/ (linux/amd64, linux/arm64)
              ├── Артефакт checksums (привязан к run-id)
              └── GitHub pre-release build-{N}

  Параллельно, после успешного CI:
  ├── gateway-deploy.yml  → gateway.nomad
  ├── xauth-deploy.yml    → xauth.nomad
  ├── xhttp-deploy.yml    → xhttp.nomad
  └── xws-deploy.yml      → xws.nomad
```

Каждый сервис деплоится независимо. Ошибка деплоя xauth не блокирует деплой gateway.

На каждый push в `main` — автоматический rolling update.
Versioned-релизы создаются по тегу `v*` (`release.yml`) — для ручного/rollback деплоя.

### Как работает generic деплой

`ci.yml` публикует `checksums.env` как GitHub Actions artifact. Каждый `*-deploy.yml`:
1. Скачивает `checksums.env` по `run-id` триггернувшего CI-запуска
2. Загружает checksums в env (`cat checksums.env >> $GITHUB_ENV`)
3. Вызывает composite action `.github/actions/deploy-nomad`

Composite action:
1. SSH → определяет arch ноды (`uname -m`)
2. Читает объявленные переменные из `.nomad` файла (`grep variable`)
3. Для каждой переменной берёт значение из env вызывающего workflow через `${!VAR}`
4. Запускает `nomad job run` с этими переменными
5. `nomad job run` без `-detach` — блокирует до завершения деплоя, возвращает ошибку при откате

---

### Добавить новый сервис

**Четыре шага, `ci.yml` и composite action не трогать:**

**1. Написать сервис**

```
cmd/newservice/main.go
internal/services/newservice/
```

CI автоматически подхватит его при следующем push — `shopt -s nullglob; for dir in cmd/*/`.

**2. Создать Nomad-джоб**

```
deployments/infra/nomad/newservice.nomad
```

Правила:
- Все переменные объявляются в **ВЕРХНЕМ РЕГИСТРЕ** — имена совпадают с GitHub Secrets
- Обязательные переменные (общие для всех сервисов): `GITHUB_REPO`, `VERSION`, `ARCH`, `CHECKSUM`, `NATS_USER`, `NATS_PASSWORD`, `LOG_LEVEL`
- Сервис-специфичные переменные: любые нужные (`DATABASE_URL`, `API_KEY` и т.д.)

Пример:
```hcl
variable "GITHUB_REPO" { default = "" }
variable "VERSION"     { default = "" }
variable "ARCH"        { default = "amd64" }
variable "CHECKSUM"    { default = "" }
variable "NATS_USER"   { default = "" }
variable "NATS_PASSWORD" { default = "" }
variable "LOG_LEVEL"   { default = "info" }
variable "MY_API_KEY"  { default = "" }  # сервис-специфичная

job "newservice" {
  ...
  group "newservice" {
    # Dynamic port для /healthz. Probe идёт через NATS-mux сервиса
    # (nc.RegisterHealth) — ловит deadlock-в-handler, не только process exit.
    # См. P-M9 в audit/STATUS.md.
    network {
      port "health" {}
    }

    service {
      name     = "newservice"
      port     = "health"
      provider = "nomad"

      check {
        name     = "http-health"
        type     = "http"
        path     = "/healthz"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "newservice" {
      driver = "raw_exec"
      user   = "nomad"  # ОБЯЗАТЕЛЬНО: tasks не должны бегать от root (см. I-H6).
                        # Системный user 'nomad' создаётся в setup.sh.
      artifact {
        source      = "https://github.com/${var.GITHUB_REPO}/releases/download/${var.VERSION}/newservice_linux_${var.ARCH}.tar.gz"
        destination = "local/"

        # ВАЖНО: checksum живёт внутри options { ... }, не как direct attribute
        # блока artifact (см. I-C4). Без options-обёртки nomad job validate
        # отбивает с `Invalid block definition`.
        options {
          checksum = var.CHECKSUM
        }
      }
      env {
        NATS_HOST   = "127.0.0.1"
        NATS_USER   = var.NATS_USER
        MY_API_KEY  = var.MY_API_KEY
        HEALTH_ADDR = "${NOMAD_IP_health}:${NOMAD_PORT_health}"  # обязательно для nc.RegisterHealth
        ...
      }
    }
  }
}
```

В `main.go` сервиса:

```go
healthAddr := os.Getenv("HEALTH_ADDR")
if healthAddr == "" {
    log.Fatal().Msg("HEALTH_ADDR не задан")
}
// ... NewClient ...
healthSrv, err := natsClient.RegisterHealth("newservice", healthAddr)
if err != nil {
    log.Fatal().Err(err).Msg("health")
}
// В shutdown (ДО natsClient.Drain):
healthSrv.Shutdown(ctx)
```

**3. Создать deploy workflow**

Скопировать любой существующий `*-deploy.yml`, изменить:
- `name:` — имя workflow
- `env:` блок — только секреты этого сервиса
- `nomad_file:` — имя `.nomad` файла

```yaml
# .github/workflows/newservice-deploy.yml
name: Deploy newservice

on:
  workflow_run:
    workflows: [CI]
    types: [completed]
    branches: [main]

jobs:
  deploy:
    if: ${{ github.event.workflow_run.conclusion == 'success' }}
    runs-on: ubuntu-latest

    env:
      DEPLOY_SSH_KEY:  ${{ secrets.DEPLOY_SSH_KEY }}
      DEPLOY_USER:     ${{ secrets.DEPLOY_USER }}
      PLATFORM_DOMAIN: ${{ secrets.PLATFORM_DOMAIN }}
      NOMAD_TOKEN:     ${{ secrets.NOMAD_TOKEN }}
      NATS_USER:       ${{ secrets.NATS_USER }}
      NATS_PASSWORD:   ${{ secrets.NATS_PASSWORD }}
      MY_API_KEY:      ${{ secrets.MY_API_KEY }}   # сервис-специфичный

    steps:
      - uses: actions/checkout@v4

      - name: Download checksums
        uses: actions/download-artifact@v4
        with:
          name: checksums
          run-id: ${{ github.event.workflow_run.id }}
          github-token: ${{ secrets.GITHUB_TOKEN }}

      - name: Load checksums into env
        run: cat checksums.env >> "$GITHUB_ENV"

      - uses: ./.github/actions/deploy-nomad
        with:
          nomad_file: newservice.nomad
          version: build-${{ github.event.workflow_run.run_number }}
```

**4. Добавить секреты в GitHub Secrets**

`Settings → Secrets and variables → Actions → New repository secret`

Добавить сервис-специфичные секреты, объявленные в `.nomad` файле.
Общие секреты (`DEPLOY_SSH_KEY`, `NATS_USER` и др.) уже есть — добавлять не нужно.

---

### GitHub Secrets

`Settings → Secrets and variables → Actions`

#### Общие (используются всеми сервисами)

| Secret            | Описание |
|-------------------|----------|
| `DEPLOY_SSH_KEY`  | Приватный Ed25519-ключ для SSH |
| `DEPLOY_USER`     | SSH-пользователь (`ubuntu`) |
| `PLATFORM_DOMAIN` | Домен A-записей кластера (`nodes.example.com`). CI резолвит все A-записи, деплоит через первую доступную ноду. |
| `NOMAD_TOKEN`     | Nomad ACL bootstrap-токен (`uuidgen`). Задаётся один раз. |
| `NATS_USER`       | Логин NATS |
| `NATS_PASSWORD`   | Пароль NATS |
| `NATS_CA_KEY`     | CA приватный ключ в base64 (только для setup.sh) |
| `NATS_CA_CERT`    | CA сертификат в base64 (только для setup.sh) |

#### Gateway (`gateway-deploy.yml`)

| Secret                     | Описание |
|----------------------------|----------|
| `ALLOWED_HOSTS`            | Разрешённые HTTP Origin (`example.com,api.example.com`) |
| `GATEWAY_AUTH_RATE_PREFIX` | URL-префикс жёсткого rate limit (`/v1/xauth/`). Пусто — отключён. |
| `GATEWAY_TRUSTED_PROXY`    | IP балансировщика для X-Real-IP. Пусто при DNS round-robin. |

#### xauth (`xauth-deploy.yml`)

| Secret               | Описание |
|----------------------|----------|
| `AUTH_USERNAME`      | Логин пользователя |
| `AUTH_PASSWORD`      | Пароль пользователя |
| `AUTH_ACCESS_SECRET` | HMAC-ключ access JWT (`openssl rand -hex 32`) |
| `AUTH_REFRESH_SECRET`| HMAC-ключ refresh JWT (`openssl rand -hex 32`) |
| `COOKIE_DOMAIN`      | Домен для Set-Cookie (`.example.com`) |

#### xhttp (`xhttp-deploy.yml`)

| Secret          | Описание |
|-----------------|----------|
| `DATABASE_URL`  | PostgreSQL DSN (`postgres://user:pass@host:5432/db?sslmode=require`) |
| `ACCESS_SECRET` | HMAC-ключ валидации JWT — **должен совпадать с `AUTH_ACCESS_SECRET`** |

#### GitHub Variables (необязательные)

`Settings → Secrets and variables → Actions → Variables`. Не маскируются в логах.

| Variable             | По умолчанию | Описание |
|----------------------|--------------|----------|
| `COOKIE_SECURE`      | `true`       | `false` только при HTTP-разработке без HTTPS |
| `AUTH_ACCESS_TTL`    | `15m`        | Время жизни access JWT |
| `AUTH_REFRESH_TTL`   | `168h`       | Время жизни refresh JWT (7 дней) |
| `INACTIVITY_TIMEOUT` | `3m`         | Таймаут неактивной WebSocket-сессии |
| `CACHE_TTL`          | `30s`        | TTL NATS KV кэша в xhttp |

---

### Первоначальная настройка секретов

Сгенерировать CA для NATS TLS (один раз, локально):
```bash
openssl genrsa -out nats-ca.key 4096
openssl req -new -x509 -key nats-ca.key -out nats-ca.crt -days 3650 \
  -subj "/CN=platform-nats-ca/O=platform"

# Добавить в GitHub Secrets:
# NATS_CA_KEY  = $(base64 -w0 < nats-ca.key)
# NATS_CA_CERT = $(base64 -w0 < nats-ca.crt)

# Удалить локальные файлы — CA-ключ нигде не хранится!
rm nats-ca.key nats-ca.crt
```

Сгенерировать SSH-ключ:
```bash
ssh-keygen -t ed25519 -f deploy_key -N ""
# deploy_key.pub → добавить на сервер при создании VPS (панель провайдера)
# deploy_key     → GitHub Secret DEPLOY_SSH_KEY
rm deploy_key deploy_key.pub
```

Сгенерировать HMAC-ключи:
```bash
openssl rand -hex 32  # AUTH_ACCESS_SECRET (= ACCESS_SECRET в xhttp)
openssl rand -hex 32  # AUTH_REFRESH_SECRET
```

Сгенерировать Nomad токен:
```bash
uuidgen  # → NOMAD_TOKEN
```

> Значения секретов могут содержать любые символы — ограничений нет.

---

### Настройка ноды через GitHub Actions (альтернатива)

Вместо ручного запуска `setup.sh` можно использовать `setup.yml`:

```
Actions → Setup VPS → Run workflow
```

Поля: `node_ip` (публичный IP ноды), `platform_domain`, `host_fingerprint` (опционально — SHA256-fingerprint host key, см. I-H5; пусто = TOFU).
Все секреты берутся из GitHub Secrets — ничего вводить вручную не нужно.

---

### Ручной деплой / rollback

SSH на любую ноду:

```bash
ssh user@node

# Откатиться на конкретную версию (pre-release):
nomad job run \
  -var GITHUB_REPO=owner/repo \
  -var VERSION=build-42 \
  -var NATS_USER=... \
  ... \
  /opt/platform/deployments/infra/nomad/gateway.nomad

# Или откатить все сервисы до стабильного тега:
# NODES — число ready-нод кластера; для x-сервисов определяет количество копий
# (count = min(NODES, 3)). CI подставляет автоматически из Nomad API; при ручном
# деплое считаем так же.
NODES=$(curl -sf http://127.0.0.1:4646/v1/nodes | jq '[.[] | select(.Status=="ready")] | length')
for f in /opt/platform/deployments/infra/nomad/*.nomad; do
  nomad job run -var VERSION=v1.2.3 -var NODES=$NODES ... $f
done
```

> **Legacy (только для миграции с до-2026-04-18 архитектуры).** Если на ноде ещё крутятся монолитные джобы из старой структуры — остановите их перед первым деплоем новой:
> ```bash
> nomad job stop platform    # был platform.nomad
> nomad job stop xservices   # был xservices.nomad
> ```
> Для свежих установок этот шаг не нужен — `prod.vars.example` уже содержит 4 раздельных команды (gateway/xauth/xhttp/xws).

---

## Масштабирование

Добавление ноды — те же 3 шага что выше. Никаких изменений в конфигах.

`PLATFORM_DOMAIN` DNS-запись с новым IP → NATS и Nomad автоматически обнаруживают ноду.

### Количество копий x-сервисов

Каждый x-сервис (xauth, xhttp, xws) разворачивается в `count = min(NODES, 3)` копий с
ограничением `distinct_hosts` (копии на разных нодах). Правило:

- 1 нода — 1 копия,
- 2 ноды — 2 копии (по одной на ноду),
- 3+ нод — 3 копии (достаточная избыточность, лишние ноды остаются свободны).

`NODES` определяется автоматически на prod-сервере в момент деплоя через Nomad API
(`/v1/nodes` → число ready-нод) — CI о количестве нод не знает. При добавлении или
удалении ноды следующий деплой подхватит актуальное значение; при желании пересчитать
сразу — перезапустить джобы вручную (см. пример выше).

Gateway работает на всех нодах (`type = "system"`), поэтому отдельно не масштабируется.

### Разные дата-центры — first-class сценарий

Ноды могут жить в **разных дата-центрах, регионах, облачных провайдерах** —
платформа на это никак не реагирует и не требует никаких отдельных настроек.

Один и тот же набор конфигов работает идентично:

- `setup.sh` — один скрипт, одинаковые аргументы (`PLATFORM_DOMAIN`, `NATS_USER`/`PASSWORD`).
- `nomad.hcl` — один файл, без per-DC ветвлений; `raft_multiplier=5` уже выставлен
  с расчётом на cross-DC latency и WAN-jitter (см. комментарий в server-блоке).
- `nats.conf` — один файл, mTLS работает между любыми сетями; cluster routes
  через `nats-route://$PLATFORM_DOMAIN:6222` находят ноды в любом DC.
- `/etc/nomad/env`, `/etc/nats/env` — те же значения переменных везде.

**Что меняется при добавлении ноды в новом DC:** только запись `A` в DNS
(`PLATFORM_DOMAIN` → IP новой ноды). Ни конфиги, ни env-файлы существующих
нод править не нужно — они узнают о новой ноде через DNS-discovery
автоматически.

**Требования к сети между DC:**

- Открыты порты 4222/6222/4646/4647/4648 между подсетями всех нод (см. таблицу Firewall выше).
- Нет требований к latency или jitter — `raft_multiplier=5` поглощает разумные
  колебания (cross-region 50-300ms, transient loss до нескольких процентов).
- Разные провайдеры/IP-планы допустимы — `cluster_advertise: $NODE_IP` (NATS) и
  `advertise { http/rpc/serf = ... }` (Nomad) дают каждой ноде явно адресовать себя
  публичным IP, NAT-traversal не требуется.

---

## Ресурсы на ноде

| Компонент    | RAM     |
|--------------|---------|
| OS + Kernel  | ~150 MB |
| Nomad + NATS | ~100 MB |
| Swap         | авто    |
| Сервисы      | остаток |

---

## Firewall (межнодовые порты)

| Порт      | Протокол | Назначение              |
|-----------|----------|-------------------------|
| 4222      | TCP      | NATS клиент             |
| 6222      | TCP      | NATS кластеризация      |
| 4647–4648 | TCP/UDP  | Nomad RPC / Serf gossip |
| 8080      | TCP      | Gateway (внешний)       |

В production ограничьте порты 4222/6222/4647-4648 диапазоном IP ваших нод. Nomad HTTP API (4646) и NATS monitoring (8222) слушают только на 127.0.0.1 — между нодами не нужны, доступ — через SSH-tunnel.

---

## Безопасность кластера NATS

Cluster-трафик (порт 6222) защищён только **mTLS** (`cluster { tls { verify: true } }`),
без `cluster { authorization { user/password } }`. Это сознательное accept-risk решение.

**Что защищает текущая конфигурация:**

- `verify: true` — нода без валидного cert, подписанного нашим CA, не подключается к кластеру.
- CA-ключ хранится только в GitHub Secret `NATS_CA_KEY`. На серверах его нет ни на диске,
  ни во временных файлах: `setup.sh` подаёт его в openssl через process substitution
  (`<(printf '%s\n' "$NATS_CA_KEY")`, см. I-M5). Без CA-ключа подделать новый node.crt
  невозможно.
- node-cert per-host: при компрометации одной ноды атакующий получает один cert,
  не может выдать себе новый и не может прикинуться другой нодой.

**Почему cluster authorization не добавлен:**

Defense-in-depth (mTLS + пароль) защищал бы единственный сценарий:
«утёк один `node.key`, но `/etc/nats/env` не утёк». На практике оба файла лежат
на одном диске, под одной chmod 600, и читаются только при root-доступе —
если злоумышленник получил root, он получит и cert, и пароль одновременно.

Дополнительный пароль увеличивает поверхность ошибок (ещё один секрет в env,
ещё один пункт ротации, риск рассинхрона между нодами при обновлении), не давая
реальной защиты от существующих threat-векторов.

**Когда стоит пересмотреть:**

- Multi-tenant модель (несколько изолированных кластеров на одном PLATFORM_DOMAIN).
- Хранение `node.key` отдельно от `/etc/nats/env` (например, на HSM/KMS) — тогда
  утечка диска без cert становится реальной, и cluster password её закроет.
- Compliance-требования (PCI/SOC2 «defence in depth»), где наличие двух независимых
  cluster-secrets обязательно по чек-листу аудита.
