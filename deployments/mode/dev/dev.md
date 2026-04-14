# Локальная разработка с Nomad

## Предварительные требования

- [Docker](https://docs.docker.com/get-docker/)
- [Nomad](https://developer.hashicorp.com/nomad/downloads)
- Go

## Запуск

```bash
# 1 нода (NATS single-node + Nomad -dev)
./deployments/mode/dev/start.sh

# 3 ноды (NATS cluster + Nomad cluster из 3 агентов)
./deployments/mode/dev/start.sh 3

# Остановить всё
./deployments/mode/dev/start.sh stop
```

Скрипт автоматически:
1. Поднимает NATS-кластер и PostgreSQL через Docker Compose
2. Собирает бинарники (`go build ./cmd/...`) в `bin/`
3. Запускает Nomad (dev-режим для 1 ноды, кластер для N > 1)
4. Дожидается готовности кластера и деплоит джобы

## После запуска

| Сервис       | Адрес                         |
|--------------|-------------------------------|
| Gateway      | http://localhost:8080         |
| Nomad UI     | http://localhost:4646         |
| NATS Monitor | http://localhost:8222         |

```bash
nomad job status platform    # статус gateway
nomad job status xservices   # статус демо-сервисов
nomad alloc logs <alloc-id>  # логи конкретной аллокации
```

Health check: `curl http://localhost:8080/health`

## Переменные окружения

Значения берутся из `dev.vars`. Для изменения отредактируйте файл и перезапустите джобы:

```bash
nomad job run \
  -var-file=deployments/mode/dev/dev.vars \
  -var="binary_dir=$PWD/bin" \
  deployments/services/nomad/platform.nomad
```

## Проверка поведения кластера (N > 1)

### Rolling update

```bash
# Пересобрать и переразвернуть
go build -o bin/gateway ./cmd/gateway
nomad job run \
  -var-file=deployments/mode/dev/dev.vars \
  -var="binary_dir=$PWD/bin" \
  deployments/services/nomad/platform.nomad

nomad deployment list   # наблюдать за rolling update
```

### Self-healing

```bash
# Убить процесс gateway — Nomad должен перезапустить его
pgrep -f 'bin/gateway' | xargs kill
nomad alloc status      # через несколько секунд — новая аллокация
```

### Падение ноды Nomad

```bash
# Посмотреть PIDs агентов
cat /tmp/platform-dev-pids

# Убить одну ноду
kill <PID>

# Nomad перераспределит аллокации на оставшихся нодах
nomad node status
```

## Логи

| Компонент      | Файл                              |
|----------------|-----------------------------------|
| Nomad (1 нода) | `/tmp/platform-dev-nomad.log`     |
| Nomad нода N   | `/tmp/platform-dev-nomad-N.log`   |
| Сервисы        | `nomad alloc logs <alloc-id>`     |
