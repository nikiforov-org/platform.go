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

**Cloudflare:** обязательно выключить Proxy (серое облако, DNS only). Порты 6222 и 4222 Cloudflare не проксирует — NATS должен коннектиться напрямую к IP ноды.

**Сертификат на `PLATFORM_DOMAIN` не нужен.** Домен используется только для DNS-резолвинга: NATS получает список IP нод и после этого коннектится напрямую к каждому IP. TLS-сертификаты выписаны на IP ноды (`SAN: IP:x.x.x.x`), а не на доменное имя.

Одновременно можно добавить A-запись публичного домена (для балансировки — см. раздел ниже).

### 3. Запустить одну команду на сервере

```bash
wget -qO- https://raw.githubusercontent.com/OWNER/REPO/main/deployments/envs/prod/setup.sh \
  | PLATFORM_DOMAIN=nodes.example.com \
    NATS_USER=nats \
    NATS_PASSWORD=secret \
    NATS_CA_KEY="$(base64 -w0 < nats-ca.key)" \
    NATS_CA_CERT="$(base64 -w0 < nats-ca.crt)" \
    NOMAD_CA_KEY="$(base64 -w0 < nomad-ca.key)" \
    NOMAD_CA_CERT="$(base64 -w0 < nomad-ca.crt)" \
    NOMAD_GOSSIP_KEY="$(openssl rand -base64 32)" \
    NOMAD_TOKEN=$(uuidgen) \
    bash
```

`NOMAD_GOSSIP_KEY` и `NOMAD_TOKEN` достаточно сгенерировать один раз и
переиспользовать для всех нод кластера (см. «Первоначальная настройка
секретов» ниже). Альтернатива — запуск через `setup.yml`-workflow с
GitHub Secrets, см. ниже.

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

> **Рекомендация для продакшена на несколько нод: используйте балансировщик нагрузки (Вариант 2).**
>
> DNS round-robin (Вариант 1) прост в настройке и работает, но имеет принципиальные
> ограничения, которые проявляются именно в многонодовом продакшене:
>
> - **Нет health-проверок.** Если нода упала, DNS продолжает отдавать её IP до истечения TTL.
>   Клиенты, закешировавшие этот IP (браузеры, мобильные ОС, CDN), получают ошибки
>   до нескольких минут — пока не пересмотрят DNS-запись. Балансировщик видит падение
>   за секунды и выводит ноду из ротации немедленно.
> - **Rate limiter работает по IP балансировщика, а не клиента.** Без `GATEWAY_TRUSTED_PROXY`
>   Gateway не умеет различать клиентов за NAT или CDN — rate limit будет срабатывать
>   на весь трафик с одного IP-адреса, включая легитимных пользователей. С балансировщиком
>   и правильным `GATEWAY_TRUSTED_PROXY` Gateway доверяет `X-Real-IP` только от LB
>   и видит реальный IP каждого клиента.
> - **Неравномерная нагрузка.** DNS-клиенты кешируют ответ и «прилипают» к одному IP
>   на время жизни кеша — распределение зависит от TTL и поведения клиента, а не от
>   реальной загрузки нод. Балансировщик распределяет по активным соединениям или
>   запросам.
> - **Роллинг-деплой.** Nomad обновляет ноды по одной, но в момент рестарта задачи на
>   ноде Gateway кратковременно недоступен. Балансировщик с health-проверкой исключает
>   ноду из ротации на это время; DNS round-robin продолжает слать на неё трафик.
>
> DNS round-robin оправдан на одной ноде или как дополнительный уровень отказоустойчивости
> поверх балансировщика (несколько A-записей на LB-инстансы).

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

На платформе может работать **любой статически слинкованный Linux-бинарник** — не только Go-сервисы из этого репозитория. Единственное требование: бинарник подключается к локальному NATS (`127.0.0.1:4222`, credentials из env) и подписывается на нужные subjects. Docker не нужен и не установлен. Артефакт может лежать на любом URL — GitHub Releases, S3, собственный сервер; Nomad скачает и проверит checksum перед запуском.

**Четыре шага, `ci.yml` и composite action не трогать:**

**1. Написать сервис** (или подготовить внешний бинарник)

Если сервис собирается в этом репозитории:
```
cmd/newservice/main.go
internal/services/newservice/
```
CI автоматически подхватит его при следующем push — `shopt -s nullglob; for dir in cmd/*/`.

Если сервис внешний — просто укажите URL бинарника в `artifact {}` на шаге 2.

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

`Settings → Secrets and variables → Actions → Secrets`

#### Общие для всех deploy-workflow

| Secret              | Как получить | Описание |
|---------------------|--------------|----------|
| `DEPLOY_SSH_KEY`    | `ssh-keygen -t ed25519 -f deploy_key -N ""` → файл `deploy_key` | Приватный Ed25519-ключ; публичный (`deploy_key.pub`) добавить на сервер при создании VPS |
| `DEPLOY_USER`       | имя пользователя на сервере | SSH-пользователь (`ubuntu`, `up` и т.д.) |
| `PLATFORM_DOMAIN`   | ваш DNS-домен | Домен A-записей кластера (`nodes.example.com`). CI резолвит все A-записи, деплоит через первую доступную ноду. |
| `NOMAD_TOKEN`       | `uuidgen` | Nomad ACL bootstrap-токен. Задаётся один раз, одинаковый для всего кластера. |
| `HOST_FINGERPRINTS` | `ssh-keyscan -t ed25519 <NODE_IP> \| ssh-keygen -lf -` | SHA256-fingerprint(s) прод-нод (по одному на строку или через запятую). Пусто — TOFU-режим без MITM-защиты. |
| `NATS_USER`         | придумать | Логин NATS-сервера |
| `NATS_PASSWORD`     | `openssl rand -hex 16` | Пароль NATS-сервера |

#### Только для `setup.yml` (настройка новой ноды)

| Secret              | Как получить | Описание |
|---------------------|--------------|----------|
| `NATS_CA_KEY`       | `openssl genrsa -out nats-ca.key 4096` → `base64 -w0 < nats-ca.key` | CA приватный ключ NATS. Генерируется один раз; после записи в Secret — удалить локально. |
| `NATS_CA_CERT`      | `openssl req -new -x509 -key nats-ca.key -days 3650 ...` → `base64 -w0 < nats-ca.crt` | CA сертификат NATS (публичный). |
| `NOMAD_CA_KEY`      | `openssl genrsa -out nomad-ca.key 4096` → `base64 -w0 < nomad-ca.key` | CA приватный ключ Nomad. Генерируется один раз; после записи в Secret — удалить локально. |
| `NOMAD_CA_CERT`     | `openssl req -new -x509 -key nomad-ca.key -days 3650 ...` → `base64 -w0 < nomad-ca.crt` | CA сертификат Nomad (публичный). |
| `NOMAD_GOSSIP_KEY`  | `openssl rand -base64 32` | 32-байтный симметричный ключ Serf-шифрования. Одинаковый для всех нод кластера. |

Полная инструкция генерации — раздел «Первоначальная настройка секретов» ниже.

#### Gateway (`gateway-deploy.yml`)

| Secret                     | Как получить | Описание |
|----------------------------|--------------|----------|
| `ALLOWED_HOSTS`            | ваш домен | Разрешённые HTTP Origin через запятую (`example.com,api.example.com`) |
| `GATEWAY_AUTH_RATE_PREFIX` | `/v1/xauth/` | URL-префикс строгого rate limit для auth-эндпоинтов. Пусто — отключён. |
| `GATEWAY_TRUSTED_PROXY`    | IP LB или пусто | IP балансировщика для X-Real-IP. Пусто при DNS round-robin. |

#### xauth — демо-сервис (`xauth-deploy.yml`)

| Secret               | Как получить | Описание |
|----------------------|--------------|----------|
| `AUTH_USERNAME`      | придумать | Логин пользователя |
| `AUTH_PASSWORD`      | придумать | Пароль пользователя |
| `AUTH_ACCESS_SECRET` | `openssl rand -hex 32` | HMAC-ключ подписи access JWT |
| `AUTH_REFRESH_SECRET`| `openssl rand -hex 32` | HMAC-ключ подписи refresh JWT |
| `COOKIE_DOMAIN`      | `.example.com` | Домен для Set-Cookie (с точкой — работает на поддоменах) |

#### xhttp — демо-сервис (`xhttp-deploy.yml`)

| Secret          | Как получить | Описание |
|-----------------|--------------|----------|
| `DATABASE_URL`  | от PostgreSQL-провайдера | DSN: `postgres://user:pass@host:5432/db?sslmode=require` |
| `ACCESS_SECRET` | = значение `AUTH_ACCESS_SECRET` | HMAC-ключ валидации JWT. **Должен совпадать с `AUTH_ACCESS_SECRET`** |

#### xws — демо-сервис (`xws-deploy.yml`)

Дополнительных секретов нет — использует только секреты из раздела «Общие».

---

### GitHub Variables (необязательные)

`Settings → Secrets and variables → Actions → Variables`. Значения видны в логах (не маскируются).

| Variable             | По умолчанию | Описание |
|----------------------|--------------|----------|
| `COOKIE_SECURE`      | `true`       | Установить `false` при разработке без HTTPS |
| `AUTH_ACCESS_TTL`    | `15m`        | Время жизни access JWT |
| `AUTH_REFRESH_TTL`   | `168h`       | Время жизни refresh JWT (7 дней) |
| `INACTIVITY_TIMEOUT` | `3m`         | Таймаут неактивной WebSocket-сессии (xws) |
| `CACHE_TTL`          | `30s`        | TTL NATS KV кэша (xhttp) |

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

Сгенерировать CA для Nomad TLS (один раз, локально):
```bash
openssl genrsa -out nomad-ca.key 4096
openssl req -new -x509 -key nomad-ca.key -out nomad-ca.crt -days 3650 \
  -subj "/CN=platform-nomad-ca/O=platform"

# Добавить в GitHub Secrets:
# NOMAD_CA_KEY  = $(base64 -w0 < nomad-ca.key)
# NOMAD_CA_CERT = $(base64 -w0 < nomad-ca.crt)

# Удалить локальные файлы — CA-ключ нигде не хранится!
rm nomad-ca.key nomad-ca.crt
```

Сгенерировать gossip-key Nomad (один раз, для всего кластера):
```bash
openssl rand -base64 32  # → NOMAD_GOSSIP_KEY
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

**Несколько изолированных кластеров** (отдельные продукты или команды) — также поддерживаются.
Каждый кластер требует:

| Переменная | Зачем уникальна |
|-----------|-----------------|
| `PLATFORM_DOMAIN` | Граница Nomad `retry_join` — ноды с разным DNS не войдут в один Raft |
| `NATS_CA_KEY` / `NATS_CA_CERT` | Ноды с разными CA не смогут установить mTLS (6222) |
| `NOMAD_CA_KEY` / `NOMAD_CA_CERT` | Ноды с разными CA не смогут установить RPC TLS (4647) |
| `NOMAD_GOSSIP_KEY` | Разные ключи — Serf-пакеты чужого кластера не расшифруются |

Один и тот же `setup.sh` запускается с разными значениями переменных — никаких изменений
в конфигах или коде не нужно.

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
- Сертификаты выписаны на IP ноды (`SAN: IP:x.x.x.x`), не на доменное имя — сертификат
  на `PLATFORM_DOMAIN` не нужен и не используется.

**Почему cluster authorization не добавлен:**

Defense-in-depth (mTLS + пароль) защищал бы единственный сценарий:
«утёк один `node.key`, но `/etc/nats/env` не утёк». На практике оба файла лежат
на одном диске, под одной chmod 600, и читаются только при root-доступе —
если злоумышленник получил root, он получит и cert, и пароль одновременно.

Дополнительный пароль увеличивает поверхность ошибок (ещё один секрет в env,
ещё один пункт ротации, риск рассинхрона между нодами при обновлении), не давая
реальной защиты от существующих threat-векторов.

**Когда стоит пересмотреть:**

- Хранение `node.key` отдельно от `/etc/nats/env` (например, на HSM/KMS) — тогда
  утечка диска без cert становится реальной, и cluster password её закроет.
- Compliance-требования (PCI/SOC2 «defence in depth»), где наличие двух независимых
  cluster-secrets обязательно по чек-листу аудита.

---

## Безопасность кластера Nomad

Между нодами Nomad использует два протокола:

- **RPC (4647)** — Raft-консенсус и job specs (включая `NOMAD_VAR_*`-секреты:
  `NATS_PASSWORD`, `AUTH_ACCESS_SECRET`, `AUTH_REFRESH_SECRET`, `DATABASE_URL`).
- **Serf (4648)** — gossip-протокол: список нод, статусы, leadership-сигналы.

При развёртывании в разных ДЦ через публичный интернет оба канала идут через WAN.
Без шифрования атакующий с MITM перехватывает секреты непрерывно (24/7).

**RPC — TLS** (`tls { rpc = true; verify_server_hostname = true }`):

- Сертификат ноды подписан CA, ключ CA хранится только в GitHub Secret
  `NOMAD_CA_KEY`. На серверах его нет ни на диске, ни во временных файлах:
  `setup.sh` подаёт его в `openssl x509 -CAkey` через process substitution
  (`<(printf '%s\n' "$NOMAD_CA_KEY")`). Без CA-ключа подделать новый node.crt
  невозможно.
- node-cert per-host: при компрометации одной ноды атакующий получает один cert,
  не может выдать себе новый и не может прикинуться другой нодой.
- SAN node-cert: `DNS:server.global.nomad,DNS:client.global.nomad,IP:NODE_IP,IP:127.0.0.1`.
  DNS-имена обязательны для `verify_server_hostname=true` — Nomad валидирует
  cert пира именно по этому SAN (region по умолчанию `global`).

**Serf — gossip-key** (`server { encrypt = "${NOMAD_GOSSIP_KEY}" }`):

- Это отдельный механизм, не TLS: Serf через TLS у Nomad не работает.
  Симметричный 32-байтный ключ, одинаковый для всех нод; передаётся через
  systemd-env-файл `/etc/nomad/env` (chmod 600).
- При ротации ключа: использовать `nomad operator gossip keyring rotate`
  (online-rotation без downtime).

**HTTP API (4646) остаётся plain HTTP** (`tls.http = false`, default):

- API слушает только на 127.0.0.1 (см. `addresses { http = "127.0.0.1" }`),
  наружу не выходит — threat model cross-DC WAN MITM на него не распространяется.
- Trade-off: все локальные `curl http://127.0.0.1:4646/...` (ACL bootstrap,
  healthcheck wait-loop в `setup.sh`, probe в `deploy-nomad/action.yml`)
  и `nomad job run` остаются на plain HTTP без cert env vars.
