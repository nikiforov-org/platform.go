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

---

## Балансировка трафика

Доступно два варианта — они не исключают друг друга.

### Вариант 1: Пассивная балансировка через DNS (round-robin)

Добавьте A-записи публичного домена для каждой ноды с Gateway:

| Тип | Имя              | Значение  |
|-----|------------------|-----------|
| A   | `api.example.com` | `IP_1`   |
| A   | `api.example.com` | `IP_2`   |
| A   | `api.example.com` | `IP_3`   |

DNS-клиенты случайно выбирают один из IP. Без единой точки отказа, бесплатно.
`GATEWAY_TRUSTED_PROXY` не нужен.

### Вариант 2: Балансировщик нагрузки (managed LB)

Один A-запись публичного домена → IP балансировщика:

| Тип | Имя              | Значение  |
|-----|------------------|-----------|
| A   | `api.example.com` | `LB_IP`  |

LB проксирует трафик на все ноды (:8080) и проставляет `X-Real-IP`.
Задайте `GATEWAY_TRUSTED_PROXY = IP_LB` в GitHub Secrets — Gateway будет
доверять `X-Real-IP` только от LB, защищая rate limiter от спуфинга.

---

## CI/CD (GitHub Actions)

### Схема

```
push → main
  └── ci  (build + vet + test)
        └── deploy  (только если CI прошёл)
              ├── Сборка бинарников (linux/amd64, linux/arm64)
              ├── GitHub pre-release build-{N}
              └── SSH → git clone/pull + nomad job run
```

На каждый push в `main` происходит автоматический rolling update.
Versioned-релизы создаются по тегу `v*` (`release.yml`) — для ручного/rollback деплоя.

### GitHub Secrets

Задаются в `Settings → Secrets and variables → Actions`:

| Secret                    | Используется в | Описание |
|---------------------------|----------------|----------|
| `DEPLOY_SSH_KEY`          | setup, deploy  | Приватный Ed25519-ключ |
| `DEPLOY_USER`             | setup, deploy  | SSH-пользователь (`ubuntu`) |
| `PLATFORM_DOMAIN`         | setup, deploy  | Домен A-записей кластера (`nodes.example.com`). CI резолвит все A-записи и перебирает ноды — нет единой точки отказа. |
| `NATS_USER`               | setup, deploy  | Логин NATS |
| `NATS_PASSWORD`           | setup, deploy  | Пароль NATS |
| `NATS_CA_KEY`             | setup          | CA приватный ключ в base64 (`base64 -w0 < nats-ca.key`) |
| `NATS_CA_CERT`            | setup          | CA сертификат в base64 (`base64 -w0 < nats-ca.crt`) |
| `ALLOWED_HOSTS`           | deploy         | Разрешённые Origin (`example.com,api.example.com`) |
| `GATEWAY_AUTH_RATE_PREFIX`| deploy         | URL-префикс жёсткого rate limit (`/v1/xauth/`) |
| `GATEWAY_TRUSTED_PROXY`   | deploy         | IP LB для X-Real-IP (пусто при DNS round-robin) |
| `AUTH_USERNAME`           | deploy         | Логин xauth |
| `AUTH_PASSWORD`           | deploy         | Пароль xauth |
| `AUTH_ACCESS_SECRET`      | deploy         | HMAC-ключ access JWT (`openssl rand -hex 32`) |
| `AUTH_REFRESH_SECRET`     | deploy         | HMAC-ключ refresh JWT (`openssl rand -hex 32`) |
| `COOKIE_DOMAIN`           | deploy         | Домен Set-Cookie (`.example.com`) |
| `DATABASE_URL`            | deploy         | PostgreSQL DSN |
| `NOMAD_TOKEN`             | deploy         | Nomad ACL bootstrap-токен — UUID (`uuidgen`), задаётся один раз |

### GitHub Variables (необязательные)

Задаются в `Settings → Secrets and variables → Actions → Variables`. Не маскируются в логах.
Если не заданы — используются дефолтные значения.

| Variable | По умолчанию | Описание |
|----------|--------------|----------|
| `COOKIE_SECURE` | `true` | `false` только при HTTP-разработке без HTTPS |
| `AUTH_ACCESS_TTL` | `15m` | Время жизни access JWT |
| `AUTH_REFRESH_TTL` | `168h` | Время жизни refresh JWT (7 дней) |
| `INACTIVITY_TIMEOUT` | `3m` | Таймаут неактивной WebSocket-сессии |
| `CACHE_TTL` | `30s` | TTL NATS KV кэша в xhttp |

Сгенерировать CA для NATS TLS (один раз, локально):
```bash
openssl genrsa -out nats-ca.key 4096
openssl req -new -x509 -key nats-ca.key -out nats-ca.crt -days 3650 \
  -subj "/CN=platform-nats-ca/O=platform"

# Добавить в GitHub Secrets:
# NATS_CA_KEY  = $(base64 -w0 < nats-ca.key)
# NATS_CA_CERT = $(base64 -w0 < nats-ca.crt)

# Удалить локальные файлы после добавления в Secrets!
rm nats-ca.key nats-ca.crt
```

CA-ключ нигде не хранится: setup.sh использует его только для подписи сертификата ноды и немедленно удаляет.

Сгенерировать SSH-ключ:
```bash
ssh-keygen -t ed25519 -f deploy_key -N ""
# deploy_key.pub → добавить на сервер при создании VPS (панель провайдера)
# deploy_key     → GitHub Secret DEPLOY_SSH_KEY
```

> Значения секретов могут содержать любые символы включая `$`, `\` и `"`.

### Альтернатива: запуск setup.sh через GitHub Actions

```
Actions → Setup VPS → Run workflow
```
Поля: `node_ip`, `platform_domain`, `install_postgres`.
Секреты берутся из GitHub Secrets — ничего не вводится вручную.

### Ручной деплой / rollback

```bash
cp deployments/envs/prod/prod.vars.example deployments/envs/prod/prod.vars
# Заполнить prod.vars, указать version = "build-N" или "v1.2.3"

nomad job run -var-file=deployments/envs/prod/prod.vars deployments/infra/nomad/platform.nomad
nomad job run -var-file=deployments/envs/prod/prod.vars deployments/infra/nomad/xservices.nomad
```

---

## Масштабирование

Добавление ноды — те же 3 шага что выше. Никаких изменений в конфигах.

`PLATFORM_DOMAIN` DNS-запись с новым IP → NATS и Nomad автоматически обнаруживают ноду.

---

## Ресурсы на ноде

| Компонент     | RAM      |
|---------------|----------|
| OS + Kernel   | ~150 MB  |
| Nomad + NATS  | ~100 MB  |
| Swap          | авто     |
| Сервисы       | остаток  |

---

## Firewall (межнодовые порты)

| Порт      | Протокол | Назначение              |
|-----------|----------|-------------------------|
| 4222      | TCP      | NATS клиент             |
| 6222      | TCP      | NATS кластеризация      |
| 4646      | TCP      | Nomad HTTP API          |
| 4647–4648 | TCP/UDP  | Nomad RPC / Serf gossip |
| 8080      | TCP      | Gateway (внешний)       |

В production ограничьте порты 4222/6222/4646-4648 диапазоном IP ваших нод.
