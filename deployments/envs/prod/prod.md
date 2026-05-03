# Production-деплой

## Архитектура

Каждая нода запускает:
- **NATS** — часть кластера, связанного через DNS Discovery (`PLATFORM_DOMAIN`)
- **Nomad** — hybrid server+client, управляет сервисами через raw_exec
- **Go-бинарники** — запускаются Nomad напрямую, без Docker

Трафик: DNS → Gateway (:80) → NATS → сервисы.

---

## Добавление новой ноды — 3 шага

### 1. Купить VPS

Ubuntu 22.04 или 24.04. SSH-ключ из GitHub Secret `PLATFORM_DEPLOY_SSH_KEY` должен быть
добавлен в `~/.ssh/authorized_keys` при создании (через панель провайдера).

### 2. Добавить A-запись DNS

Добавьте A-запись вашего кластерного домена (`PLATFORM_DOMAIN`) → IP новой ноды.

| Тип | Имя                   | Значение    |
|-----|-----------------------|-------------|
| A   | `nodes.example.com`   | `IP_НОДЫ`   |

**Cloudflare:** обязательно выключить Proxy (серое облако, DNS only). Порты 6222 и 4222 Cloudflare не проксирует — NATS должен коннектиться напрямую к IP ноды.

**Сертификат на `PLATFORM_DOMAIN` не нужен.** Домен используется для DNS-резолвинга (список нод) и для TLS SAN — NATS при cluster handshake проверяет, что cert содержит `DNS:$PLATFORM_DOMAIN`. Node-сертификаты идентичны на всех нодах (multi-DC): `SAN: DNS:nodes.example.com,IP:127.0.0.1` — без привязки к конкретному IP ноды.

Одновременно можно добавить A-запись публичного домена (для балансировки — см. раздел ниже).

### 3. Запустить workflow

```
Actions → Setup VPS → Run workflow
```

Поля:
- **node_ip** — публичный IP новой ноды
- **platform_domain** — домен A-записей (`nodes.example.com`)
- **host_fingerprint** — SHA256-fingerprint SSH host key ноды. Получить: `ssh-keyscan -t ed25519 <IP> | ssh-keygen -lf -` или из консоли провайдера. Пусто — TOFU-режим.

Все секреты берутся из GitHub Secrets автоматически.

> **host_fingerprint задаётся при каждом запуске Setup VPS** — у каждой ноды свой fingerprint. Это не глобальная настройка кластера. Deploy-workflow (`*-deploy.yml`) используют отдельную переменную `PLATFORM_HOST_FINGERPRINTS` (GitHub Variable) со списком fingerprint'ов всех нод. При добавлении новой ноды допишите её fingerprint в `PLATFORM_HOST_FINGERPRINTS`.

Workflow (~2-3 мин):
- Настраивает оптимальный swap (2×RAM, ≤10% диска, ≤4 GB)
- Устанавливает Nomad и NATS
- Создаёт systemd-сервисы с credentials в `/etc/nats/env`, `/etc/nomad/env` (chmod 600)
- Настраивает ufw
- Запускает сервисы

**После завершения нода автоматически находит остальные ноды через DNS
и входит в кластер — неважно, первая ли это нода или двадцать первая.**

---

## NATS Кластеризация

### Архитектура

- **setup.sh** устанавливает ноды **всегда в standalone-режиме** — JetStream работает сразу
- **GitHub Actions workflow** (`clustering.yml`) управляет кластеризацией централизованно

### Автоматический режим (по умолчанию)

При деплое через CI/CD workflow `clustering.yml` запускается автоматически после успешного деплоя:

1. Получает список нод из DNS (`nodes.<PLATFORM_DOMAIN>`)
2. Определяет текущее состояние кластера (все standalone / все кластер / смешанное)
3. Выполняет необходимые действия:
   - **1 нода** → ничего (standalone OK)
   - **2+ ноды без cluster.conf** → создаёт `/etc/nats/cluster.conf` на всех, добавляет `include "cluster.conf"` в `/etc/nats/nats.conf`, делает `systemctl reload nats`
   - **2+ ноды, часть с cluster.conf** → создаёт конфиг только на новых нодах
   - **Все с cluster.conf** → ничего (кластер уже настроен)

**Никакого SSH между нодами не требуется** — вся логика в GitHub Actions.

### Ручной запуск

Если нужно вручную перекластеризовать или исправить проблему:

1. Перейти в GitHub: **Actions → NATS Clustering**
2. Нажать **"Run workflow"** → выбрать branch `main` → **"Run workflow"**
3. Дождаться завершения (логи доступны в UI)

### Проверка состояния кластера

```bash
# На любой ноде:
curl -s http://127.0.0.1:8222/healthz
# Ожидается: {"status":"ok"}

# Для кластера (2+ ноды):
curl -s http://127.0.0.1:8222/varz | jq '{
  cluster: .cluster.num_routes,
  js_leader: .jetstream.meta_cluster.leader
}'

# Ожидаемые значения:
# - cluster: N-1 (для N нод в DNS)
# - js_leader: IP одной из нод
```

### Добавление новой ноды

1. Добавить A-запись для новой ноды в `nodes.<PLATFORM_DOMAIN>`
2. Запустить `setup.sh` на новой ноде (нода стартует в standalone)
3. Закоммитить любое изменение в `main` или вручную запустить `clustering.yml`
4. Workflow настроит кластер автоматически

### Удаление ноды

1. Удалить A-запись из `nodes.<PLATFORM_DOMAIN>`
2. Остановить сервисы на удаляемой ноде: `systemctl stop nomad nats`
3. Если осталась 1 нода — она продолжит работать с `cluster.conf` (кворум 1/1)
4. Если осталось 2+ ноды — кластер перестроится автоматически

### Troubleshooting

**Workflow failed при кластеризации:**
- Проверить логи в GitHub Actions UI
- Частые причины: SSH недоступен на одной из нод, DNS не обновился
- Исправить проблему, перезапустить workflow вручную

**JetStream unavailable после кластеризации:**
- Проверить `systemctl status nats` на всех нодах
- Проверить `journalctl -u nats -n 100` — искать ошибки TLS/routing
- Проверить firewall: порт 6222/TCP должен быть открыт между нодами

**Нода не видит другие ноды в кластере:**
- `curl -s http://127.0.0.1:8222/varz | jq .cluster.routes`
- Проверить что DNS резолвит все A-записи: `dig +short nodes.<PLATFORM_DOMAIN>`
- Проверить mTLS сертификаты: все ноды должны иметь cert от одного CA

---

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
> Дождаться завершения workflow первой ноды, только потом запускать для следующей.
> Когда кластер уже работает — порядок добавления не важен.

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
> - **Rate limiter работает по IP балансировщика, а не клиента.** Без `PLATFORM_GATEWAY_TRUSTED_PROXY`
>   Gateway не умеет различать клиентов за NAT или CDN — rate limit будет срабатывать
>   на весь трафик с одного IP-адреса, включая легитимных пользователей. С балансировщиком
>   и правильным `PLATFORM_GATEWAY_TRUSTED_PROXY` Gateway доверяет `X-Real-IP` только от LB
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
`PLATFORM_GATEWAY_TRUSTED_PROXY` не нужен.

### Вариант 2: Балансировщик нагрузки (managed LB)

Один A-запись публичного домена → IP балансировщика:

| Тип | Имя               | Значение  |
|-----|-------------------|-----------|
| A   | `api.example.com` | `LB_IP`   |

LB проксирует трафик на все ноды (:80) и проставляет `X-Real-IP`.
Задайте `PLATFORM_GATEWAY_TRUSTED_PROXY = IP_LB` в GitHub Secrets — Gateway будет
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
- Обязательные переменные (общие для всех сервисов): `GITHUB_REPO`, `VERSION`, `ARCH`, `CHECKSUM`, `PLATFORM_NATS_USER`, `PLATFORM_NATS_PASSWORD`, `PLATFORM_LOG_LEVEL`
- Сервис-специфичные переменные: любые нужные (`X_HTTP_DATABASE_URL`, `API_KEY` и т.д.)

Пример:
```hcl
variable "GITHUB_REPO" { default = "" }
variable "VERSION"     { default = "" }
variable "ARCH"        { default = "amd64" }
variable "CHECKSUM"    { default = "" }
variable "PLATFORM_NATS_USER"   { default = "" }
variable "PLATFORM_NATS_PASSWORD" { default = "" }
variable "PLATFORM_LOG_LEVEL"   { default = "info" }
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
        PLATFORM_NATS_HOST   = "127.0.0.1"
        PLATFORM_NATS_USER   = var.PLATFORM_NATS_USER
        MY_API_KEY  = var.MY_API_KEY
        X_HEALTH_ADDR = "${NOMAD_IP_health}:${NOMAD_PORT_health}"  # обязательно для nc.RegisterHealth
        ...
      }
    }
  }
}
```

В `main.go` сервиса:

```go
healthAddr := os.Getenv("X_HEALTH_ADDR")
if healthAddr == "" {
    log.Fatal().Msg("X_HEALTH_ADDR не задан")
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
      PLATFORM_DEPLOY_SSH_KEY:  ${{ secrets.PLATFORM_DEPLOY_SSH_KEY }}
      PLATFORM_DEPLOY_USER:     ${{ vars.PLATFORM_DEPLOY_USER }}
      PLATFORM_DOMAIN:          ${{ vars.PLATFORM_DOMAIN }}
      PLATFORM_NOMAD_TOKEN:     ${{ secrets.PLATFORM_NOMAD_TOKEN }}
      PLATFORM_NATS_USER:       ${{ vars.PLATFORM_NATS_USER }}
      PLATFORM_NATS_PASSWORD:   ${{ secrets.PLATFORM_NATS_PASSWORD }}
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
Общие секреты и переменные (`PLATFORM_DEPLOY_SSH_KEY`, `PLATFORM_NATS_USER` и др.) уже есть — добавлять не нужно.

---

### Соглашение об именовании переменных окружения

Все переменные окружения в проекте следуют строгому соглашению о префиксах для наглядности и организации:

**Префикс `PLATFORM_`** — платформенные компоненты:
- Инфраструктура: NATS, Nomad, деплой, DNS, сертификаты
- Gateway и HTTP-сервер
- Общие настройки: логирование, таймауты

Примеры:
- `PLATFORM_NATS_USER`, `PLATFORM_NATS_PASSWORD` — авторизация NATS
- `PLATFORM_NOMAD_TOKEN`, `PLATFORM_NOMAD_CA_KEY` — Nomad ACL и TLS
- `PLATFORM_GATEWAY_RATE_LIMIT`, `PLATFORM_ALLOWED_HOSTS` — настройки Gateway
- `PLATFORM_DEPLOY_SSH_KEY`, `PLATFORM_DEPLOY_USER` — деплой
- `PLATFORM_HTTP_ADDR`, `PLATFORM_LOG_LEVEL` — общие настройки

**Префикс `X_`** — демонстрационные сервисы (xauth, xhttp, xws):
- Не являются частью платформы — только примеры использования
- В реальных проектах заменяются собственными микросервисами
- Настройки специфичны для конкретного сервиса

Примеры:
- `X_AUTH_USERNAME`, `X_AUTH_PASSWORD` — учётные данные xauth
- `X_AUTH_ACCESS_SECRET`, `X_AUTH_COOKIE_DOMAIN` — JWT и куки
- `X_HTTP_DATABASE_URL`, `X_HTTP_CACHE_TTL` — PostgreSQL и кэш
- `X_WS_INACTIVITY_TIMEOUT` — таймауты WebSocket

**GitHub Secrets vs Variables:**

**Secrets** (маскируются в логах) — для чувствительных данных:
- Криптоматериалы: `*_CA_KEY`, `*_CA_CERT`, `*_GOSSIP_KEY`
- Пароли и токены: `*_PASSWORD`, `*_TOKEN`, `*_SECRET`
- Приватные ключи: `*_SSH_KEY`
- Connection strings с паролями: `X_HTTP_DATABASE_URL`

**Variables** (видны в логах) — для публичных настроек:
- Имена пользователей: `PLATFORM_DEPLOY_USER`, `X_AUTH_USERNAME`
- Домены: `PLATFORM_DOMAIN`, `PLATFORM_ALLOWED_HOSTS`, `X_AUTH_COOKIE_DOMAIN`
- Таймауты и лимиты: `X_AUTH_ACCESS_TTL`, `X_HTTP_CACHE_TTL`
- IP-адреса: `PLATFORM_GATEWAY_TRUSTED_PROXY`
- Настройки безопасности: `X_AUTH_COOKIE_SECURE`, `X_AUTH_COOKIE_SAMESITE`

**Правило добавления новой переменной:**
1. Определи категорию: платформа или демо-сервис → выбери префикс
2. Определи тип данных: секрет или публичная настройка
3. Объяви в соответствующих файлах: `.nomad`, `*-deploy.yml`, Go config, документация
4. Для Secrets: добавь в GitHub → Settings → Secrets → Actions → New repository secret
5. Для Variables: добавь в GitHub → Settings → Secrets → Variables → New repository variable

---

### GitHub Secrets

`Settings → Secrets and variables → Actions → Secrets`

#### Общие для всех deploy-workflow

**Secrets:**

| Имя              | Как получить | Описание |
|------------------|--------------|----------|
| `PLATFORM_DEPLOY_SSH_KEY`    | `ssh-keygen -t ed25519 -f deploy_key -N ""` → файл `deploy_key` | Приватный Ed25519-ключ; публичный (`deploy_key.pub`) добавить на сервер при создании VPS |
| `PLATFORM_NOMAD_TOKEN`       | `uuidgen` | Nomad ACL bootstrap-токен. Задаётся один раз, одинаковый для всего кластера. |
| `PLATFORM_NATS_PASSWORD`     | `openssl rand -hex 16` | Пароль NATS-сервера |

**Variables:**

| Имя              | Значение | Описание |
|------------------|----------|----------|
| `PLATFORM_DEPLOY_USER`       | имя пользователя на сервере | SSH-пользователь (`ubuntu`, `up` и т.д.) |
| `PLATFORM_DOMAIN`   | ваш DNS-домен | Домен A-записей кластера (`nodes.example.com`). CI резолвит все A-записи, деплоит через первую доступную ноду. |
| `PLATFORM_NATS_USER`         | придумать | Логин NATS-сервера |

#### Только для `setup.yml` (настройка новой ноды)

| Secret              | Как получить | Описание |
|---------------------|--------------|----------|
| `PLATFORM_NATS_CA_KEY`       | содержимое `nats-ca.key` (PEM) | CA приватный ключ NATS. Генерируется один раз; после записи в Secret — удалить локально. |
| `PLATFORM_NATS_CA_CERT`      | содержимое `nats-ca.crt` (PEM) | CA сертификат NATS (публичный). |
| `PLATFORM_NOMAD_CA_KEY`      | содержимое `nomad-ca.key` (PEM) | CA приватный ключ Nomad. Генерируется один раз; после записи в Secret — удалить локально. |
| `PLATFORM_NOMAD_CA_CERT`     | содержимое `nomad-ca.crt` (PEM) | CA сертификат Nomad (публичный). |
| `PLATFORM_NOMAD_GOSSIP_KEY`  | `openssl rand -base64 32` | 32-байтный симметричный ключ Serf-шифрования. Одинаковый для всех нод кластера. |

Полная инструкция генерации — раздел «Первоначальная настройка секретов» ниже.

#### Gateway (`gateway-deploy.yml`)

**Variables:**

| Имя                     | Пример значения | Описание |
|-------------------------|-----------------|----------|
| `PLATFORM_ALLOWED_HOSTS`            | `example.com,api.example.com` | Разрешённые HTTP Origin через запятую |
| `PLATFORM_GATEWAY_AUTH_RATE_PREFIX` | `/v1/xauth/` | URL-префикс строгого rate limit для auth-эндпоинтов. Пусто — отключён. |
| `PLATFORM_GATEWAY_TRUSTED_PROXY`    | `1.2.3.4` или пусто | IP балансировщика для X-Real-IP. Пусто при DNS round-robin. |

#### xauth — демо-сервис (`xauth-deploy.yml`)

**Secrets:**

| Имя               | Как получить | Описание |
|-------------------|--------------|----------|
| `X_AUTH_PASSWORD`      | придумать | Пароль пользователя |
| `X_AUTH_ACCESS_SECRET` | `openssl rand -hex 32` | HMAC-ключ подписи access JWT |
| `X_AUTH_REFRESH_SECRET`| `openssl rand -hex 32` | HMAC-ключ подписи refresh JWT |

**Variables:**

| Имя               | Пример значения | Описание |
|-------------------|-----------------|----------|
| `X_AUTH_USERNAME`      | `admin` | Логин пользователя |
| `X_AUTH_COOKIE_DOMAIN` | `.example.com` | Домен для Set-Cookie (с точкой — работает на поддоменах) |
| `X_AUTH_COOKIE_SECURE` | `true` | Флаг Secure для кук (требует HTTPS) |
| `X_AUTH_COOKIE_SAMESITE` | `none` | SameSite-политика (`strict`, `lax`, `none`) |
| `X_AUTH_ACCESS_TTL`    | `15m` | Время жизни access-токена |
| `X_AUTH_REFRESH_TTL`   | `168h` | Время жизни refresh-токена (7 дней) |

#### xhttp — демо-сервис (`xhttp-deploy.yml`)

**Secrets:**

| Имя          | Как получить | Описание |
|--------------|--------------|----------|
| `X_HTTP_DATABASE_URL`  | от PostgreSQL-провайдера | DSN: `postgres://user:pass@host:5432/db?sslmode=require` |
| `X_AUTH_ACCESS_SECRET` | общий с xauth | HMAC-ключ для проверки JWT (тот же, что в xauth) |

**Variables:**

| Имя          | Пример значения | Описание |
|--------------|-----------------|----------|
| `X_HTTP_CACHE_TTL` | `30s` | TTL записей в NATS KV-кэше |

#### xws — демо-сервис (`xws-deploy.yml`)

Дополнительных секретов нет — использует только общие секреты платформы.

**Variables:**

| Имя          | Пример значения | Описание |
|--------------|-----------------|----------|
| `X_WS_INACTIVITY_TIMEOUT` | `3m` | Таймаут закрытия неактивной WS-сессии |

---

### GitHub Variables (необязательные)

`Settings → Secrets and variables → Actions → Variables`. Значения видны в логах (не маскируются).

| Variable             | По умолчанию | Описание |
|----------------------|--------------|----------|
| `X_AUTH_COOKIE_SECURE`      | `true`       | Установить `false` при разработке без HTTPS |
| `X_AUTH_ACCESS_TTL`    | `15m`        | Время жизни access JWT |
| `X_AUTH_REFRESH_TTL`   | `168h`       | Время жизни refresh JWT (7 дней) |
| `X_WS_INACTIVITY_TIMEOUT` | `3m`         | Таймаут неактивной WebSocket-сессии (xws) |
| `X_HTTP_CACHE_TTL`          | `30s`        | TTL NATS KV кэша (xhttp) |

---

### Первоначальная настройка секретов

Сгенерировать CA для NATS TLS (один раз, локально):
```bash
openssl genrsa -out nats-ca.key 4096
openssl req -new -x509 -key nats-ca.key -out nats-ca.crt -days 3650 \
  -subj "/CN=platform-nats-ca/O=platform"

# GitHub Secret PLATFORM_NATS_CA_KEY — вставить содержимое файла целиком (только ключ, не сертификат):
cat nats-ca.key

# GitHub Secret PLATFORM_NATS_CA_CERT — вставить содержимое файла целиком (только сертификат, не ключ):
cat nats-ca.crt

# Удалить локальные файлы — CA-ключ нигде не хранится!
rm nats-ca.key nats-ca.crt
```

Сгенерировать CA для Nomad TLS (один раз, локально):
```bash
openssl genrsa -out nomad-ca.key 4096
openssl req -new -x509 -key nomad-ca.key -out nomad-ca.crt -days 3650 \
  -subj "/CN=platform-nomad-ca/O=platform"

# GitHub Secret PLATFORM_NOMAD_CA_KEY — вставить содержимое файла целиком (только ключ, не сертификат):
cat nomad-ca.key

# GitHub Secret PLATFORM_NOMAD_CA_CERT — вставить содержимое файла целиком (только сертификат, не ключ):
cat nomad-ca.crt

# Удалить локальные файлы — CA-ключ нигде не хранится!
rm nomad-ca.key nomad-ca.crt
```

Сгенерировать gossip-key Nomad (один раз, для всего кластера):
```bash
openssl rand -base64 32  # → PLATFORM_NOMAD_GOSSIP_KEY
```

Сгенерировать SSH-ключ:
```bash
ssh-keygen -t ed25519 -f deploy_key -N ""
# deploy_key.pub → добавить на сервер при создании VPS (панель провайдера)
# deploy_key     → GitHub Secret PLATFORM_DEPLOY_SSH_KEY
rm deploy_key deploy_key.pub
```

Сгенерировать HMAC-ключи:
```bash
openssl rand -hex 32  # X_AUTH_ACCESS_SECRET (общий для xauth и xhttp)
openssl rand -hex 32  # X_AUTH_REFRESH_SECRET
```

Сгенерировать Nomad токен:
```bash
uuidgen  # → PLATFORM_NOMAD_TOKEN
```

> Значения секретов могут содержать любые символы — ограничений нет.

---

### Ручной деплой / rollback

SSH на любую ноду:

```bash
ssh user@node

# Откатиться на конкретную версию (pre-release):
nomad job run \
  -var GITHUB_REPO=owner/repo \
  -var VERSION=build-42 \
  -var PLATFORM_NATS_USER=... \
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

- `setup.sh` — один скрипт, одинаковые аргументы (`PLATFORM_DOMAIN`, `PLATFORM_NATS_USER`/`PASSWORD`).
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
| `PLATFORM_NATS_CA_KEY` / `PLATFORM_NATS_CA_CERT` | Ноды с разными CA не смогут установить mTLS (6222) |
| `PLATFORM_NOMAD_CA_KEY` / `PLATFORM_NOMAD_CA_CERT` | Ноды с разными CA не смогут установить RPC TLS (4647) |
| `PLATFORM_NOMAD_GOSSIP_KEY` | Разные ключи — Serf-пакеты чужого кластера не расшифруются |

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
| 80        | TCP      | Gateway (внешний)       |

В production ограничьте порты 4222/6222/4647-4648 диапазоном IP ваших нод. Nomad HTTP API (4646) и NATS monitoring (8222) слушают только на 127.0.0.1 — между нодами не нужны, доступ — через SSH-tunnel.

---

## Безопасность кластера NATS

Cluster-трафик (порт 6222) защищён только **mTLS** (`cluster { tls { verify: true } }`),
без `cluster { authorization { user/password } }`. Это сознательное accept-risk решение.

**Что защищает текущая конфигурация:**

- `verify: true` — нода без валидного cert, подписанного нашим CA, не подключается к кластеру.
- CA-ключ хранится только в GitHub Secret `PLATFORM_NATS_CA_KEY`. На серверах его нет ни на
  постоянном диске: `setup.sh` пишет его в `/dev/shm` (tmpfs, RAM), подписывает
  node.crt и сразу удаляет. Без CA-ключа подделать новый node.crt
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
  `PLATFORM_NATS_PASSWORD`, `X_AUTH_ACCESS_SECRET`, `X_AUTH_REFRESH_SECRET`, `X_HTTP_DATABASE_URL`).
- **Serf (4648)** — gossip-протокол: список нод, статусы, leadership-сигналы.

При развёртывании в разных ДЦ через публичный интернет оба канала идут через WAN.
Без шифрования атакующий с MITM перехватывает секреты непрерывно (24/7).

**RPC — TLS** (`tls { rpc = true; verify_server_hostname = true }`):

- Сертификат ноды подписан CA, ключ CA хранится только в GitHub Secret
  `PLATFORM_NOMAD_CA_KEY`. На серверах его нет ни на постоянном диске: `setup.sh`
  пишет его в `/dev/shm` (tmpfs, RAM), подписывает node.crt и сразу удаляет.
  Без CA-ключа подделать новый node.crt
  невозможно.
- node-cert per-host: при компрометации одной ноды атакующий получает один cert,
  не может выдать себе новый и не может прикинуться другой нодой.
- SAN node-cert: `DNS:server.global.nomad,DNS:client.global.nomad,IP:127.0.0.1`.
  DNS-имена обязательны для `verify_server_hostname=true` — Nomad валидирует
  cert пира именно по этому SAN (region по умолчанию `global`). IP ноды НЕ включается —
  в multi-DC каждая нода имеет свой IP, node-cert идентичен на всех нодах.

**Serf — gossip-key** (`server { encrypt = "${PLATFORM_NOMAD_GOSSIP_KEY}" }`):

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

---

## Доступ к Nomad UI

Nomad UI слушает на `127.0.0.1:4646` (loopback) — снаружи недоступен. Доступ через SSH-tunnel.

### Получение токена

**При первом развёртывании кластера** (или после сброса ACL):

```bash
# На любой prod-ноде
ssh user@node
export NOMAD_ADDR=http://127.0.0.1:4646
nomad acl bootstrap
```

Команда выдаст `Secret ID` — это management-токен с полными правами. Сохранить в GitHub Secret `PLATFORM_NOMAD_TOKEN` и в безопасное хранилище (1Password, Vault).

**Если токен уже есть в GitHub Secrets:**

`Settings → Secrets and variables → Actions → Secrets → PLATFORM_NOMAD_TOKEN` — значение и есть bootstrap-токен.

### Открыть UI в браузере

**Шаг 1:** Открыть SSH-tunnel (в отдельном окне терминала):

```bash
ssh -L 4646:127.0.0.1:4646 user@node
```

Оставить это окно **открытым** — tunnel активен пока сессия жива.

**Шаг 2:** Открыть браузер:

```
http://localhost:4646
```

**Шаг 3:** В правом верхнем углу нажать **"Sign In"** → вставить токен из `PLATFORM_NOMAD_TOKEN`.

### CLI-доступ через tunnel

Если нужен `nomad` CLI с локальной машины (вместо SSH на prod):

```bash
# В отдельном окне терминала — tunnel
ssh -L 4646:127.0.0.1:4646 user@node

# В другом окне
export NOMAD_ADDR=http://localhost:4646
export PLATFORM_NOMAD_TOKEN=<значение из GitHub Secret>
nomad status
nomad ui -authenticate  # откроет браузер с автологином
```

### Ротация токена

При компрометации токена:

```bash
# На prod-ноде
sudo systemctl stop nomad
sudo rm -rf /var/lib/nomad/server/*
sudo systemctl start nomad
sleep 5
nomad acl bootstrap  # выдаст новый токен
```

**ВАЖНО:** все старые токены (включая CI/CD) станут невалидными. Обновить `PLATFORM_NOMAD_TOKEN` в GitHub Secrets сразу после bootstrap.

---

## Ротация секретов

Workflow `rotate-credentials.yml` обновляет серверные конфиги на всех нодах и автоматически
редеплоит сервисы (когда нужно).

### Процедура

**1. Обновить секрет в GitHub**

`Settings → Secrets and variables → Actions → Secrets` (или Variables для публичных настроек)
→ редактировать нужный Secret/Variable → новое значение.

**2. Запустить workflow**

```
Actions → Rotate Credentials → Run workflow
```

Выбрать что ротировать (можно несколько групп одновременно):

- **NATS user/password** — обновляет `/etc/nats/env` на всех нодах → `systemctl reload nats`
  → редеплоит все сервисы (чтобы получили новые креды).
- **NATS TLS** — генерирует новые node-сертификаты (CA-ключ используется на runner, на сервер
  не попадает) → копирует на все ноды → `systemctl restart nats`.
- **Nomad TLS** — генерирует новые node-сертификаты → копирует на все ноды →
  `systemctl restart nomad`.
- **Nomad gossip key** — обновляет `/etc/nomad/env` → `systemctl restart nomad`.

Workflow автоматически:
- Резолвит все ноды через `PLATFORM_DOMAIN` DNS
- Верифицирует SSH host keys через `PLATFORM_HOST_FINGERPRINTS` (защита от MITM)
- Последовательно обновляет каждую ноду
- Дожидается стабилизации сервисов (15s)
- При ротации NATS creds — скачивает последний `build-N` release и делает редеплой
  всех джобов (gateway/xauth/xhttp/xws) с новыми кредами

**3. Проверить**

```bash
# На любой prod-ноде
curl -s http://127.0.0.1:8222/healthz  # NATS
curl -s http://127.0.0.1:4646/v1/status/leader  # Nomad

# Проверить что сервисы работают
curl -s http://NODE_IP/health  # Gateway
```

### Какие секреты ротируются автоматически

| Секрет | Группа workflow | Файлы на сервере | Действие |
|--------|----------------|------------------|----------|
| `PLATFORM_NATS_USER`<br>`PLATFORM_NATS_PASSWORD` | NATS user/password | `/etc/nats/env` | reload NATS → редеплой сервисов |
| `PLATFORM_NATS_CA_KEY`<br>`PLATFORM_NATS_CA_CERT` | NATS TLS | `/etc/nats/ca.crt`<br>`/etc/nats/node.crt`<br>`/etc/nats/node.key` | перегенерация cert → restart NATS |
| `PLATFORM_NOMAD_CA_KEY`<br>`PLATFORM_NOMAD_CA_CERT` | Nomad TLS | `/etc/nomad/ca.crt`<br>`/etc/nomad/node.crt`<br>`/etc/nomad/node.key` | перегенерация cert → restart Nomad |
| `PLATFORM_NOMAD_GOSSIP_KEY` | Nomad gossip key | `/etc/nomad/env` | restart Nomad |

### Ротация PLATFORM_NOMAD_TOKEN (вручную)

Bootstrap-токен Nomad **не ротируется через workflow** — он записан в Raft-state, замена
требует сброса кластера.

Процедура (только при компрометации):

1. **На всех нодах остановить Nomad:**
   ```bash
   sudo systemctl stop nomad
   sudo rm -rf /var/lib/nomad/server/*
   ```

2. **Сгенерировать новый токен:**
   ```bash
   uuidgen  # новый токен
   ```

3. **Обновить GitHub Secret:**
   `Settings → Secrets → PLATFORM_NOMAD_TOKEN` → новое значение.

4. **На первой ноде:**
   ```bash
   sudo systemctl start nomad
   sleep 5
   # Проверить что лидер выбран
   curl -s http://127.0.0.1:4646/v1/status/leader
   # Bootstrap с новым токеном
   nomad acl bootstrap  # должен вернуть тот же токен, что в GitHub Secret
   ```

5. **На остальных нодах:**
   ```bash
   sudo systemctl start nomad
   ```

6. **Редеплой всех сервисов через CI:**
   Закоммитить любое изменение в `main` (или manual trigger `*-deploy.yml`) — сервисы
   получат доступ к обновлённому Nomad-кластеру.

### Ротация X-сервисных секретов

X-сервисные секреты (`X_AUTH_*`, `X_HTTP_*`) **не требуют ротации через workflow** —
они живут только в Nomad job env, на сервере не хранятся.

Процедура:

1. Обновить GitHub Secret (например, `X_AUTH_ACCESS_SECRET`)
2. Закоммитить любое изменение в `main` (триггерит CI → deploy)

Или manual trigger соответствующего `*-deploy.yml` workflow.
