# Production-деплой

## Архитектура

Каждая нода запускает:
- **NATS** — часть кластера, связанного через DNS Discovery (`nodes.up.mt`)
- **Nomad** — hybrid server+client, управляет сервисами через raw_exec
- **Go-бинарники** — запускаются Nomad напрямую, без Docker

Внешний трафик: Managed Load Balancer → Gateway (:8080) → NATS → сервисы.

## Первичная настройка серверов

### NATS

```bash
# Создать пользователя и директории
useradd -r -s /bin/false nats
mkdir -p /var/lib/nats/jetstream /etc/nats
chown -R nats:nats /var/lib/nats

# Скопировать конфиг
scp deployments/services/nats/nats.conf user@node:/etc/nats/nats.conf

# Создать systemd-юнит /etc/systemd/system/nats.service:
# [Unit]
# Description=NATS Server
# After=network.target
#
# [Service]
# User=nats
# Environment=HOSTNAME=node1
# Environment=NODE_IP=10.0.0.1
# Environment=NATS_CLUSTER_USER=cluster-user
# Environment=NATS_CLUSTER_PASSWORD=cluster-password
# ExecStart=/usr/local/bin/nats-server -c /etc/nats/nats.conf
# Restart=always
#
# [Install]
# WantedBy=multi-user.target

systemctl enable --now nats
```

### Nomad

```bash
mkdir -p /var/lib/nomad /etc/nomad
scp deployments/services/nomad/nomad.hcl user@node:/etc/nomad/nomad.hcl

# Создать systemd-юнит /etc/systemd/system/nomad.service:
# [Unit]
# Description=Nomad Agent
# After=network.target nats.service
#
# [Service]
# Environment=NOMAD_BOOTSTRAP_EXPECT=3
# ExecStart=/usr/local/bin/nomad agent -config=/etc/nomad/nomad.hcl
# Restart=always
# KillSignal=SIGINT
#
# [Install]
# WantedBy=multi-user.target

systemctl enable --now nomad
```

## Firewall (между нодами)

| Порт      | Протокол | Назначение              |
|-----------|----------|-------------------------|
| 4222      | TCP      | NATS клиент             |
| 6222      | TCP      | NATS кластеризация      |
| 4646      | TCP      | Nomad HTTP API          |
| 4647–4648 | TCP/UDP  | Nomad RPC / Serf gossip |
| 8080      | TCP      | Gateway (внешний)       |

## Деплой сервисов

```bash
# Скопировать prod.vars.example → prod.vars, заполнить реальными значениями
cp deployments/mode/prod/prod.vars.example deployments/mode/prod/prod.vars
# ... отредактировать prod.vars ...

# Задеплоить
nomad job run \
  -var-file=deployments/mode/prod/prod.vars \
  deployments/services/nomad/platform.nomad

nomad job run \
  -var-file=deployments/mode/prod/prod.vars \
  deployments/services/nomad/xservices.nomad
```

## CI/CD (GitHub Actions)

Автоматический деплой при push в `main`:

1. GitHub Actions собирает бинарники: `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/...`
2. Копирует на все ноды через `scp`: `/usr/local/bin/{gateway,xauth,xhttp,xws}`
3. Запускает `nomad job run` на одной из нод — Nomad выполняет rolling update

Rolling update: Nomad перезапускает по одной аллокации, дожидается healthy (`/health` → 200) перед следующей. Zero downtime.

## Масштабирование

Добавление ноды:
1. Запустить NATS и Nomad на новом сервере (те же конфиги)
2. Добавить A-запись `nodes.up.mt` → IP новой ноды
3. NATS и Nomad автоматически обнаружат новую ноду через DNS

Количество нод не ограничено. `NOMAD_BOOTSTRAP_EXPECT` меняется только при первичном bootstrap кластера.

## Ресурсы на ноде

| Компонент     | RAM      |
|---------------|----------|
| OS + Kernel   | 150 MB   |
| Nomad + NATS  | 100 MB   |
| Сервисы       | остаток  |

Рекомендуется создать swap-файл как буфер при пиковой нагрузке.
