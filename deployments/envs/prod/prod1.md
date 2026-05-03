# Развёртывание кластера — пошаговая инструкция

Документ написан так, чтобы читать сверху вниз без переходов по ссылкам.
Шаги 1–3 выполняются один раз при создании кластера. Шаг 4 повторяется для каждой новой ноды.

---

## Шаг 1. Генерация ключей (один раз, локально)

Выполнить на своём компьютере. Все ключи сразу пойдут в GitHub Secrets — локально не хранить.

### SSH-ключ для деплоя

```bash
ssh-keygen -t ed25519 -f deploy_key -N ""
```

Создаст два файла:
- `deploy_key` — приватный ключ (→ GitHub Secret `PLATFORM_DEPLOY_SSH_KEY`)
- `deploy_key.pub` — публичный ключ (→ добавить на VPS при создании через панель провайдера)

### CA-сертификаты NATS и Nomad

```bash
# NATS CA
openssl genrsa -out nats-ca.key 4096
openssl req -new -x509 -key nats-ca.key -out nats-ca.crt -days 3650 \
  -subj "/CN=platform-nats-ca/O=platform"

# Nomad CA
openssl genrsa -out nomad-ca.key 4096
openssl req -new -x509 -key nomad-ca.key -out nomad-ca.crt -days 3650 \
  -subj "/CN=platform-nomad-ca/O=platform"
```

### Случайные ключи

```bash
openssl rand -base64 32   # → PLATFORM_NOMAD_GOSSIP_KEY
openssl rand -hex 16      # → PLATFORM_NATS_PASSWORD
openssl rand -hex 32      # → X_AUTH_ACCESS_SECRET
openssl rand -hex 32      # → X_AUTH_REFRESH_SECRET
uuidgen                   # → PLATFORM_NOMAD_TOKEN
```

---

## Шаг 2. Настройка GitHub Secrets и Variables (один раз)

Заполнить все секреты и переменные по таблицам. После заполнения удалить локальные файлы ключей.

### Инфраструктурные Secrets

`Settings → Secrets and variables → Actions → Secrets → New repository secret`

| Secret | Значение |
|--------|----------|
| `PLATFORM_DEPLOY_SSH_KEY` | Содержимое файла `deploy_key` (приватный ключ, начинается с `-----BEGIN OPENSSH PRIVATE KEY-----`) |
| `PLATFORM_NOMAD_TOKEN` | Вывод `uuidgen` |
| `PLATFORM_NOMAD_GOSSIP_KEY` | Вывод `openssl rand -base64 32` |
| `PLATFORM_NATS_PASSWORD` | Вывод `openssl rand -hex 16` |

### Инфраструктурные Variables

`Settings → Secrets and variables → Actions → Variables → New repository variable`

| Variable | Значение |
|----------|----------|
| `PLATFORM_DEPLOY_USER` | Имя SSH-пользователя на серверах (`ubuntu`, `up` и т.д.) |
| `PLATFORM_DOMAIN` | Домен для A-записей нод, например `nodes.example.com` |
| `PLATFORM_NATS_USER` | Любой логин, например `nats` |

### CA-ключи и сертификаты

Вставлять содержимое файла целиком (всё, включая строки `-----BEGIN...-----` и `-----END...-----`).

| Secret | Команда для получения значения |
|--------|-------------------------------|
| `PLATFORM_NATS_CA_KEY` | `cat nats-ca.key` |
| `PLATFORM_NATS_CA_CERT` | `cat nats-ca.crt` |
| `PLATFORM_NOMAD_CA_KEY` | `cat nomad-ca.key` |
| `PLATFORM_NOMAD_CA_CERT` | `cat nomad-ca.crt` |

> **Важно:** `PLATFORM_NATS_CA_KEY` — только содержимое `nats-ca.key`, `PLATFORM_NATS_CA_CERT` — только содержимое `nats-ca.crt`. Не смешивать.

После заполнения секретов — удалить локальные файлы:
```bash
rm deploy_key deploy_key.pub nats-ca.key nats-ca.crt nomad-ca.key nomad-ca.crt
```

### Gateway — Variables

| Variable | Значение |
|----------|----------|
| `PLATFORM_ALLOWED_HOSTS` | Домены через запятую, с которых разрешены запросы (`example.com,api.example.com`) |
| `PLATFORM_GATEWAY_AUTH_RATE_PREFIX` | Префикс URL для строгого rate limit авторизации, например `/v1/xauth/`. Пусто — отключён. |
| `PLATFORM_GATEWAY_TRUSTED_PROXY` | IP балансировщика нагрузки, если используется. Пусто при DNS round-robin. |

### Демо-сервисы (xauth, xhttp, xws)

> Эти сервисы — учебные примеры, не production-код. Настраивать только если нужны для демонстрации.

**Secrets:**

| Secret | Значение |
|--------|----------|
| `X_AUTH_PASSWORD` | Пароль пользователя для xauth |
| `X_AUTH_ACCESS_SECRET` | Вывод `openssl rand -hex 32` |
| `X_AUTH_REFRESH_SECRET` | Вывод `openssl rand -hex 32` |
| `X_HTTP_DATABASE_URL` | Строка подключения PostgreSQL: `postgres://user:pass@host:5432/db?sslmode=require` |

**Variables:**

| Variable | Значение |
|----------|----------|
| `X_AUTH_USERNAME` | Логин пользователя для xauth |
| `X_AUTH_COOKIE_DOMAIN` | Домен для cookie с точкой: `.example.com` |
| `X_AUTH_COOKIE_SECURE` | `true` (для HTTPS) или `false` (для HTTP-разработки) |
| `X_AUTH_COOKIE_SAMESITE` | `strict`, `lax` или `none` |
| `X_AUTH_ACCESS_TTL` | Время жизни access-токена, например `15m` |
| `X_AUTH_REFRESH_TTL` | Время жизни refresh-токена, например `168h` |
| `X_HTTP_CACHE_TTL` | TTL NATS KV-кэша, например `30s` |
| `X_WS_INACTIVITY_TIMEOUT` | Таймаут неактивной WS-сессии, например `3m` |

---

## Шаг 3. Настройка DNS (один раз)

Создать домен для кластерных нод, например `nodes.example.com`.
A-записи будут добавляться по одной при каждом добавлении новой ноды (шаг 4).

**Cloudflare:** обязательно выключить Proxy (серое облако, DNS only) — NATS подключается напрямую к IP, Cloudflare-проксирование порты 4222 и 6222 не поддерживает.

---

## Шаг 4. Добавление ноды (повторять для каждой новой ноды)

### 4.1. Купить VPS

- ОС: Ubuntu 22.04 или 24.04
- При создании добавить публичный SSH-ключ (`deploy_key.pub`) через панель провайдера

### 4.2. Добавить A-запись DNS

Добавить A-запись `PLATFORM_DOMAIN` → публичный IP новой ноды:

| Тип | Имя | Значение |
|-----|-----|----------|
| A | `nodes.example.com` | `IP_НОВОЙ_НОДЫ` |

Если нод несколько — каждая нода добавляет свою A-запись к тому же домену.

### 4.3. Настроить Security List / Firewall провайдера

Открыть входящие порты между нодами:

| Порт | Протокол | Назначение |
|------|----------|------------|
| 22 | TCP | SSH |
| 80 | TCP | Gateway (публичный API) |
| 4222 | TCP | NATS клиент |
| 6222 | TCP | NATS кластер |
| 4647 | TCP+UDP | Nomad RPC |
| 4648 | TCP+UDP | Nomad Serf |

Порты 4646 (Nomad UI) и 8222 (NATS monitoring) — только через SSH-tunnel, наружу не открывать.

### 4.4. Запустить установку через GitHub Actions

```
Actions → Setup VPS → Run workflow
```

Заполнить поля:
- **node_ip** — публичный IP новой ноды
- **platform_domain** — домен A-записей (`nodes.example.com`)
- **host_fingerprint** — SHA256-fingerprint SSH host key ноды. Получить: `ssh-keyscan -t ed25519 <IP> | ssh-keygen -lf -` или из консоли провайдера. Пусто — TOFU-режим (подключение без проверки, fingerprint можно сверить по логам workflow постфактум).

> **host_fingerprint задаётся при каждом запуске Setup VPS** — у каждой ноды свой fingerprint. Это не глобальная настройка кластера и не GitHub Secret/Variable. Deploy-workflow (`*-deploy.yml`) используют отдельную переменную `PLATFORM_HOST_FINGERPRINTS` (GitHub Variable), содержащую fingerprint'ы всех нод. При добавлении новой ноды допишите её fingerprint в `PLATFORM_HOST_FINGERPRINTS` через запятую или на новой строке.

Workflow выполнится за ~2–3 минуты. После завершения нода автоматически присоединится к кластеру через DNS — никаких дополнительных действий не нужно.

> **При создании кластера с нуля:** дождаться завершения workflow для первой ноды, только потом запускать для второй. Когда кластер уже работает — порядок не важен.

### 4.5. Проверить что нода вошла в кластер

SSH на ноду и выполнить:

```bash
ssh PLATFORM_DEPLOY_USER@IP_НОДЫ

nomad server members          # все ноды в статусе alive
nomad operator raft list-peers # все ноды как Voter=true
curl http://127.0.0.1:8222/healthz  # {"status":"ok"}
```

---

## Шаг 5. Первый деплой сервисов

Сделать push в ветку `main` — CI автоматически:
1. Соберёт бинарники
2. Создаст pre-release
3. Задеплоит все сервисы через Nomad

Проверить в Nomad UI (через SSH-tunnel):
```bash
ssh -L 4646:127.0.0.1:4646 PLATFORM_DEPLOY_USER@IP_НОДЫ
# Открыть http://localhost:4646
```

---

## Откат на предыдущую версию

SSH на любую ноду:

```bash
ssh PLATFORM_DEPLOY_USER@IP_НОДЫ

NODES=$(curl -sf http://127.0.0.1:4646/v1/nodes | jq '[.[] | select(.Status=="ready")] | length')

# Откатить все сервисы до конкретной версии (build-N или v1.2.3):
for f in /opt/platform/deployments/infra/nomad/*.nomad; do
  sudo -u nomad nomad job run -var VERSION=build-42 -var NODES=$NODES \
    -var GITHUB_REPO=owner/repo -var PLATFORM_NATS_USER=... $f
done
```

---

## Добавление нового сервиса в кластер

Любой статически собранный Linux-бинарник становится частью платформы — достаточно чтобы он подключался к NATS на `127.0.0.1:4222`. Docker не нужен.

1. Написать сервис (или взять готовый бинарник с любого URL)
2. Создать Nomad-джоб `deployments/infra/nomad/newservice.nomad` — скопировать любой существующий как пример
3. Создать `.github/workflows/newservice-deploy.yml` — скопировать любой существующий `*-deploy.yml`, изменить `name` и `nomad_file`
4. Добавить сервис-специфичные GitHub Secrets
5. Сделать push в `main` — деплой произойдёт автоматически
