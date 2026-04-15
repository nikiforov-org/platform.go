# Запуск в production — что нужно сделать руками

Всё, что описано в этом документе, делается **один раз** перед первым деплоем.
После этого добавление нод и деплой кода — полностью автоматические.

---

## 1. Сгенерировать секреты (локально)

Выполните на своей машине — не на сервере.

### SSH-ключ для деплоя

```bash
ssh-keygen -t ed25519 -f deploy_key -N ""
```

Создаст два файла:
- `deploy_key` — приватный ключ (→ GitHub Secret `DEPLOY_SSH_KEY`)
- `deploy_key.pub` — публичный ключ (→ добавить на VPS при создании через панель провайдера)

### CA для NATS TLS

```bash
openssl genrsa -out nats-ca.key 4096
openssl req -new -x509 -key nats-ca.key -out nats-ca.crt -days 3650 \
  -subj "/CN=platform-nats-ca/O=platform"
```

Конвертировать в base64 для GitHub Secrets:

```bash
base64 -w0 < nats-ca.key   # → NATS_CA_KEY
base64 -w0 < nats-ca.crt   # → NATS_CA_CERT
```

После добавления в Secrets — удалить локальные файлы:

```bash
rm nats-ca.key nats-ca.crt deploy_key deploy_key.pub
```

### Nomad ACL токен

```bash
uuidgen   # → NOMAD_TOKEN
```

Это обычный UUID. Nomad принимает его как bootstrap-секрет при первом старте кластера.

### JWT-ключи для xauth

```bash
openssl rand -hex 32   # → AUTH_ACCESS_SECRET
openssl rand -hex 32   # → AUTH_REFRESH_SECRET
```

---

## 2. Добавить GitHub Secrets

`Settings → Secrets and variables → Actions → New repository secret`

| Secret | Значение | Как получить |
|--------|----------|--------------|
| `DEPLOY_SSH_KEY` | Содержимое `deploy_key` | `cat deploy_key` |
| `DEPLOY_USER` | SSH-пользователь VPS | Обычно `ubuntu` или `root` |
| `PLATFORM_DOMAIN` | Домен A-записей кластера | `nodes.example.com` |
| `NATS_USER` | Логин NATS | Придумать |
| `NATS_PASSWORD` | Пароль NATS | Придумать (без `$` и `\`) |
| `NATS_CA_KEY` | base64 CA-ключа | `base64 -w0 < nats-ca.key` |
| `NATS_CA_CERT` | base64 CA-сертификата | `base64 -w0 < nats-ca.crt` |
| `NOMAD_TOKEN` | UUID | `uuidgen` |
| `ALLOWED_HOSTS` | Разрешённые домены | `example.com,api.example.com` |
| `GATEWAY_AUTH_RATE_PREFIX` | Префикс строгого rate limit | `/v1/xauth/` |
| `GATEWAY_TRUSTED_PROXY` | IP балансировщика | Оставить пустым при DNS round-robin |
| `AUTH_USERNAME` | Логин xauth | Придумать |
| `AUTH_PASSWORD` | Пароль xauth | Придумать (без `$` и `\`) |
| `AUTH_ACCESS_SECRET` | HMAC-ключ access JWT | `openssl rand -hex 32` |
| `AUTH_REFRESH_SECRET` | HMAC-ключ refresh JWT | `openssl rand -hex 32` |
| `COOKIE_DOMAIN` | Домен для Set-Cookie | `.example.com` |
| `DATABASE_URL` | PostgreSQL DSN | `postgres://user:pass@host:5432/db?sslmode=require` |

> Значения секретов могут содержать любые символы включая `$`, `\` и `"`.

---

## 3. Добавить новую ноду

### Шаг 1 — Купить VPS

Ubuntu 22.04 или 24.04. При создании добавить публичный ключ из `deploy_key.pub`
в `~/.ssh/authorized_keys` (через панель провайдера).

### Шаг 2 — Добавить A-запись DNS

Добавьте A-запись кластерного домена (`PLATFORM_DOMAIN`) → IP новой ноды:

| Тип | Имя | Значение |
|-----|-----|----------|
| A | `nodes.example.com` | `IP_НОДЫ` |

Одновременно можно добавить A-запись публичного домена для балансировки трафика (см. раздел 4).

> **Как это работает:** `PLATFORM_DOMAIN` используется сразу в трёх местах:
> - **NATS** — DNS Discovery строит mesh-сеть из всех A-записей домена
> - **Nomad** — `server_join` находит остальные ноды кластера
> - **CI/CD** — deploy резолвит все A-записи и перебирает ноды по порядку; нет единой точки отказа

### Шаг 3 — Запустить одну команду на сервере

```bash
wget -qO- https://raw.githubusercontent.com/OWNER/REPO/main/deployments/envs/prod/setup.sh \
  | PLATFORM_DOMAIN=nodes.example.com \
    NATS_USER=nats \
    NATS_PASSWORD=secret \
    NATS_CA_KEY="$(base64 -w0 < nats-ca.key)" \
    NATS_CA_CERT="$(base64 -w0 < nats-ca.crt)" \
    NOMAD_TOKEN=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx \
    bash
```

Скрипт (~2–3 мин) настраивает swap, устанавливает Nomad и NATS, генерирует TLS-сертификат ноды, настраивает systemd и firewall, запускает сервисы.

**Первая нода или двадцать первая — команда одинаковая.** Нода автоматически находит остальные через DNS и входит в кластер.

#### Альтернатива: через GitHub Actions (без копирования CA на сервер вручную)

```
Actions → Setup VPS → Run workflow
```

Поля: `node_ip`, `platform_domain`, `install_postgres`. Секреты подтягиваются автоматически из GitHub Secrets.

---

## 4. Балансировка трафика

### DNS round-robin (рекомендуется для начала)

Добавить A-записи публичного домена для каждой ноды с Gateway:

| Тип | Имя | Значение |
|-----|-----|----------|
| A | `api.example.com` | `IP_1` |
| A | `api.example.com` | `IP_2` |

`GATEWAY_TRUSTED_PROXY` оставить пустым.

### Managed Load Balancer

Один A-запись → IP балансировщика:

| Тип | Имя | Значение |
|-----|-----|----------|
| A | `api.example.com` | `LB_IP` |

Установить `GATEWAY_TRUSTED_PROXY = IP_LB` в GitHub Secrets — Gateway будет доверять
`X-Real-IP` только от этого IP для корректной работы rate limiter.

---

## 5. Первый деплой

После настройки секретов и первой ноды — сделайте любой push в `main`.

GitHub Actions автоматически:
1. Соберёт бинарники
2. Создаст GitHub pre-release `build-N`
3. Задеплоит через SSH: `git pull` + `nomad job run`

Дальнейшие деплои — автоматически при каждом push в `main`.
