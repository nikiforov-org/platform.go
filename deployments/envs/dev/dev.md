# Локальная разработка с Nomad

## Предварительные требования

- [Docker](https://docs.docker.com/get-docker/)
- [Nomad](https://developer.hashicorp.com/nomad/downloads)
- Go

## Запуск

```bash
# 1 нода (NATS single-node + Nomad -dev)
./deployments/envs/dev/start.sh

# 3 ноды (NATS cluster + Nomad cluster из 3 агентов)
./deployments/envs/dev/start.sh 3

# Остановить всё
./deployments/envs/dev/start.sh stop
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
nomad job status gateway     # платформа
nomad job status xauth       # демо
nomad job status xhttp       # демо
nomad job status xws         # демо
nomad alloc logs <alloc-id>  # логи конкретной аллокации
```

Health check: `curl http://localhost:8080/health`

## Переменные окружения

Значения берутся из `dev.vars`. Для изменения отредактируйте файл и перезапустите окружение — `start.sh` подхватит новые значения и перегенерирует Nomad-джобы:

```bash
./deployments/envs/dev/start.sh stop
./deployments/envs/dev/start.sh           # или ./start.sh N для N нод
```

Внутри `start.sh` рендерит 4 dev-варианта джобов в `/tmp/dev-{gateway,xauth,xhttp,xws}.nomad` (с локальным путём к `./bin/*`) и запускает их через `nomad job run`. Эти временные файлы можно использовать для точечных операций (см. ниже).

## Проверка поведения кластера (N > 1)

### Rolling update

```bash
# Пересобрать и перезапустить только gateway
# (/tmp/dev-gateway.nomad уже сгенерирован start.sh при старте окружения)
go build -o bin/gateway ./cmd/gateway
nomad job run /tmp/dev-gateway.nomad

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
