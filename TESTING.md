# Инструкция по тестированию NATS clustering

## Что было исправлено

**Проблема:** При установке первой ноды NATS запускался с `cluster {}` блоком, JetStream требовал кворум → `/healthz` unavailable → setup.sh падал на таймауте.

**Решение:** setup.sh теперь устанавливает ноды в standalone-режиме, кластеризация управляется через GitHub Actions workflow.

## Изменённые файлы

- ✅ `deployments/envs/prod/setup.sh` — убран cluster-блок из HEREDOC
- ✅ `deployments/infra/nats/nats.conf` — убран cluster-блок (канонический шаблон)
- ✅ `.github/workflows/clustering.yml` — новый workflow для кластеризации
- ✅ `deployments/envs/prod/prod.md` — добавлена секция "NATS Кластеризация"
- ✅ `CLAUDE.md` — обновлена информация о деплойменте
- ✅ `TODO.md` — отражён статус решения

## Проверка на текущем сервере

Текущий сервер `3.64.192.171` уже мигрирован и работает в standalone:

```bash
ssh claude@3.64.192.171
curl http://127.0.0.1:8222/healthz
# Ожидается: {"status":"ok"}
```

## Сценарии тестирования

### Сценарий 1: Fresh install (1 нода)

```bash
# 1. Создать новый VPS или использовать локальную VM
# 2. Добавить A-запись: nodes.test.domain → IP_НОДЫ
# 3. Запустить setup.sh через GitHub Actions (Setup VPS workflow)
#    или вручную с нужными переменными

# Ожидаемый результат:
# - setup.sh завершается успешно
# - /etc/nats/nats.conf НЕ содержит cluster {}
# - /etc/nats/cluster.conf НЕ существует
# - curl http://127.0.0.1:8222/healthz → {"status":"ok"}
# - systemctl status nats → active (running)
```

### Сценарий 2: Добавление второй ноды

```bash
# 1. Добавить вторую A-запись: nodes.test.domain → IP_НОДЫ_2
# 2. Запустить setup.sh на второй ноде

# Сразу после setup.sh:
# - На обеих нодах /etc/nats/cluster.conf НЕ существует (ещё standalone)
# - curl http://127.0.0.1:8222/healthz → {"status":"ok"} на обеих

# 3. Запустить clustering.yml вручную (GitHub Actions → NATS Clustering → Run workflow)

# После workflow:
# - На обеих нодах /etc/nats/cluster.conf существует
# - curl http://127.0.0.1:8222/varz | jq .cluster.num_routes → 1 (на каждой ноде)
# - curl http://127.0.0.1:8222/varz | jq .jetstream.meta_cluster.leader → IP одной из нод
# - Workflow вывод: "✓ Кластер настроен успешно"
```

### Сценарий 3: Автоматическая кластеризация после деплоя

```bash
# 1. Настроить 2+ ноды (сценарий 2)
# 2. Закоммитить любое изменение в main (например, обновить комментарий в коде)
# 3. Дождаться завершения CI/CD

# Ожидаемый результат:
# - После успешного CI автоматически запускается clustering.yml
# - Workflow определяет состояние кластера
# - Если нужно — настраивает новые ноды
# - Логи доступны в GitHub Actions UI
```

### Сценарий 4: Проверка idempotency

```bash
# Запустить clustering.yml повторно на уже настроенном кластере

# Ожидаемый результат:
# - Workflow: "✓ Кластер уже настроен, ничего не делаем"
# - Никаких изменений на нодах
# - Exit code 0
```

## GitHub Secrets для workflow

Убедиться что существуют следующие secrets:

- `SSH_PRIVATE_KEY` — приватный ключ для root@nodes
- `PLATFORM_DOMAIN` — домен кластера (например, up.mt)
- `HOST_FINGERPRINTS` — SHA256-fingerprints нод (опционально)

Эти секреты уже используются в `ci.yml` и `*-deploy.yml`, дополнительных секретов не требуется.

## Коммит изменений

```bash
git status
git add .github/workflows/clustering.yml
git add TODO.md
git add deployments/envs/prod/setup.sh
git add deployments/infra/nats/nats.conf
git add deployments/envs/prod/prod.md
git add CLAUDE.md

git commit -m "Fix NATS single-node JetStream issue

- setup.sh устанавливает ноды в standalone-режиме
- Кластеризация через GitHub Actions workflow (clustering.yml)
- JetStream работает сразу, без ожидания второй ноды
- Автоматическая кластеризация при 2+ нодах в DNS
- Обновлена документация (prod.md, CLAUDE.md)"

git push origin main
```

После push в main:
- CI соберёт и задеплоит сервисы
- clustering.yml запустится автоматически после успешного деплоя
- Если есть 2+ ноды — настроит кластер

## Проверка после деплоя

```bash
# На любой ноде:
curl http://127.0.0.1:8222/healthz
# Ожидается: {"status":"ok"}

# Для кластера (2+ ноды):
curl -s http://127.0.0.1:8222/varz | jq .cluster.num_routes
# Ожидается: N-1 для N нод

# Логи workflow:
# GitHub → Actions → NATS Clustering → последний запуск → View logs
```

## Дополнительный фикс: Nomad advertise

**Проблема:** При миграции NATS на standalone обнаружилось что Nomad не может раскрыть `${NODE_IP}` из env-файла в HCL-конфиге (Nomad не поддерживает env-интерполяцию в advertise).

**Решение:** Изменён HEREDOC в setup.sh с `<< 'HCL'` на `<< HCL` (без кавычек), чтобы bash подставлял реальные значения `$NODE_IP` и `$PLATFORM_DOMAIN` при генерации конфига.

**Изменённые файлы:**
- `deployments/envs/prod/setup.sh` — HEREDOC без кавычек, экранирован `\${attr.unique.network.ip-address}`
- `deployments/infra/nomad/nomad.hcl` — обновлены комментарии о генерации конфига

**Проверка:**
```bash
# На ноде:
curl http://127.0.0.1:4646/v1/status/leader
# Ожидается: "IP_НОДЫ:4647"

systemctl is-active nomad
# Ожидается: active
```
