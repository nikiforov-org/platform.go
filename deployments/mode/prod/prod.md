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

## CI/CD (GitHub Actions)

Деплой происходит через GitHub Releases — без Docker Registry и без прямого доступа к серверам при деплое.

### Релиз

```bash
git tag v1.2.3
git push origin v1.2.3
```

GitHub Actions (`.github/workflows/release.yml`) автоматически:
1. Собирает бинарники для `linux/amd64` и `linux/arm64` с `CGO_ENABLED=0`
2. Упаковывает каждый в `.tar.gz` (сохраняет права на исполнение)
3. Создаёт GitHub Release с архивами

### Деплой на кластер

Nomad скачивает бинарники прямо из GitHub Releases через блок `artifact` в job-файлах.

```bash
# Скопировать шаблон и заполнить значениями
cp deployments/mode/prod/prod.vars.example deployments/mode/prod/prod.vars
# ... указать github_repo, version, секреты ...

# Задеплоить (Nomad сам скачает нужную версию)
nomad job run \
  -var-file=deployments/mode/prod/prod.vars \
  deployments/services/nomad/platform.nomad

nomad job run \
  -var-file=deployments/mode/prod/prod.vars \
  deployments/services/nomad/xservices.nomad
```

Nomad выполняет rolling update: перезапускает по одной аллокации, дожидается `GET /health → 200` перед следующей. Zero downtime.

### Откат

Достаточно указать предыдущий тег в `prod.vars` и перезапустить `nomad job run`.

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
