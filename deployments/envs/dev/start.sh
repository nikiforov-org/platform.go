#!/usr/bin/env bash
# deployments/envs/dev/start.sh
#
# Запуск и остановка dev-окружения.
#
# Использование:
#   ./start.sh          — 1 нода (NATS + Nomad)
#   ./start.sh 3        — 3 ноды
#   ./start.sh stop     — остановить всё

set -euo pipefail

# =============================================================================
# Пути
# =============================================================================
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose.yml"
VARS_FILE="$SCRIPT_DIR/dev.vars"
BIN_DIR="$ROOT_DIR/bin"
NOMAD_DATA_BASE="/tmp/platform-dev"
PID_FILE="/tmp/platform-dev-pids"
NATS_CONF_RENDERED="/tmp/platform-dev-nats.conf"

# =============================================================================
# Вывод
# =============================================================================
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}▶${NC} $*"; }
info() { echo -e "${CYAN}  $*${NC}"; }
warn() { echo -e "${YELLOW}⚠${NC} $*"; }
die()  { echo -e "${RED}✗ $*${NC}" >&2; exit 1; }

# =============================================================================
# Проверка зависимостей
# =============================================================================
check_deps() {
  local missing=()
  for cmd in docker nomad go; do
    command -v "$cmd" &>/dev/null || missing+=("$cmd")
  done
  [[ ${#missing[@]} -eq 0 ]] || die "Не найдены: ${missing[*]}. Установите и повторите."
}

# =============================================================================
# Рендер NATS-конфига по числу нод
#
# При N=1 блок cluster{} отсутствует: route `nats-route://nats:6222` при
# scale=1 резолвится в IP самого контейнера, NATS залипает на
# "Waiting for routing to be established" и JetStream не становится доступным —
# сервисы падают циклическим рестартом на init KV-бакета.
#
# При N>1 route через сервисное имя `nats` (Docker DNS вернёт IP всех
# контейнеров одного сервиса — полная mesh без правки конфига).
# =============================================================================
render_nats_conf() {
  local nodes=$1

  # server_name передаётся флагом -n из docker-compose command (см. docker-compose.yml):
  # подстановка $HOSTNAME в конфиг-файле не работает надёжно — парсер NATS
  # воспринимает container ID начинающийся с цифр как число, а форма `nats-$HOSTNAME`
  # не подставляет переменную в середине токена.
  cat > "$NATS_CONF_RENDERED" <<'CONF_HEAD'
port: 4222
http_port: 8222

jetstream {
  store_dir: "/data/jetstream"
  max_mem:   256M
  max_file:  10G
}
CONF_HEAD

  if [[ $nodes -gt 1 ]]; then
    cat >> "$NATS_CONF_RENDERED" <<'CONF_CLUSTER'

cluster {
  name:   "platform-dev"
  port:   6222
  routes: ["nats-route://nats:6222"]
}
CONF_CLUSTER
  fi
}

# =============================================================================
# Инфраструктура (NATS + PostgreSQL)
# =============================================================================
start_infra() {
  local nodes=$1
  log "Запуск инфраструктуры: $nodes нод NATS + PostgreSQL..."
  render_nats_conf "$nodes"
  export DEV_NATS_CONF="$NATS_CONF_RENDERED"
  # single-node — фиксированные порты (стабильные адреса).
  # multi-node — диапазоны (каждому реплике свой host-port).
  if [[ $nodes -gt 1 ]]; then
    export NATS_CLIENT_PORTS="4222-4322:4222"
    export NATS_HTTP_PORTS="8222-8322:8222"
  else
    unset NATS_CLIENT_PORTS NATS_HTTP_PORTS
  fi
  docker compose -f "$COMPOSE_FILE" down --remove-orphans 2>/dev/null || true
  docker compose -f "$COMPOSE_FILE" up -d --scale nats="$nodes"
  info "NATS мониторинг: http://localhost:8222"
}

# =============================================================================
# Ожидание готовности NATS (JetStream)
#
# docker compose up -d возвращается после запуска контейнера, но до готовности
# JetStream (особенно в кластерном режиме — нужен meta-group leader). Без
# ожидания сервисы стартуют раньше и получают err_code=10008 на init KV-бакета,
# что приводит к log.Fatal и циклическому рестарту в Nomad.
#
# Две последовательные проверки, чтобы не зависеть от single-probe'ов:
#
#   A. В кластере выбран meta-leader. Берём /jsz на любой живой ноде — поле
#      meta_cluster.leader не пустое. Эта проверка покрывает «кворум достигнут».
#
#   B. Конкретно первая нода (dev-nats-1) принимает JetStream. Все сервисы
#      подключаются к её host-порту (см. deploy_jobs), поэтому именно её
#      готовность критична. Используем /healthz?js-server-only=true — он
#      не требует sync c meta-leader'ом (встречается расхождение: leader
#      считает follower'а current, а сам follower на self-probe отвечает
#      «not current» и не сходит с этого состояния — 4-нодный кворум и/или
#      quirk NATS). Для init KV достаточно того, что JetStream на этой
#      ноде поднят и кластер в целом имеет лидера.
# =============================================================================
wait_nats() {
  local nodes=$1
  log "Ожидание готовности NATS (JetStream)..."
  local max_wait=60
  local elapsed=0

  # Шаг A (только multi-node): meta-leader выбран (кворум достигнут).
  # При N=1 блок cluster{} в nats.conf отсутствует → meta-raft'а нет,
  # поле meta_cluster.leader пустое всегда. Пропускаем эту проверку.
  if [[ $nodes -gt 1 ]]; then
    until docker compose -f "$COMPOSE_FILE" exec -T nats \
          wget -q -O - --timeout=2 "http://localhost:8222/jsz" 2>/dev/null \
          | grep -Eq '"leader":[[:space:]]*"[^"]'; do
      sleep 1
      elapsed=$((elapsed + 1))
      [[ $elapsed -lt $max_wait ]] || die "NATS meta-leader не выбран за ${max_wait}s"
    done
  fi

  # Шаг B: dev-nats-1 принимает JetStream (к ней подключаются все сервисы).
  until docker compose -f "$COMPOSE_FILE" exec -T --index 1 nats \
        wget -q -O /dev/null --timeout=2 "http://localhost:8222/healthz?js-server-only=true" \
        &>/dev/null; do
    sleep 1
    elapsed=$((elapsed + 1))
    [[ $elapsed -lt $max_wait ]] || die "NATS dev-nats-1 не готов за ${max_wait}s"
  done

  info "NATS готов (${elapsed}s)"
}

stop_infra() {
  log "Остановка инфраструктуры..."
  docker compose -f "$COMPOSE_FILE" down
}

# =============================================================================
# Сборка бинарников
# =============================================================================
build_binaries() {
  log "Сборка бинарников → $BIN_DIR ..."
  mkdir -p "$BIN_DIR"
  cd "$ROOT_DIR"
  # cmd/*/ с nullglob — раскрывается только в директории; файл в cmd/ (README,
  # .gitkeep, .DS_Store) не попадает в список и не ломает go build.
  shopt -s nullglob
  for dir in cmd/*/; do
    svc="${dir%/}"; svc="${svc#cmd/}"
    go build -o "$BIN_DIR/$svc" "./cmd/$svc"
    info "✓ $svc"
  done
}

# =============================================================================
# Loopback-алиасы для multi-node кластера
#
# Каждая Nomad-нода сидит на своём адресе 127.0.0.N. Это нужно чтобы gateway
# мог запускаться с `type = "system"` (как в проде — по одному на ноду) и не
# получать «адрес уже занят» на общем 127.0.0.1:8080.
#
# На Linux адреса 127.0.0.2..127.0.0.N работают из коробки (loopback /8).
# На macOS их нужно добавлять вручную через ifconfig — скрипт делает это сам,
# спрашивая sudo один раз. При остановке те же алиасы убираются.
#
# Если алиас уже существует от предыдущего запуска, `ifconfig -alias` убирает
# его без ошибки (молча), потом `ifconfig alias` создаёт заново.
# =============================================================================
setup_loopback_aliases() {
  local nodes=$1
  [[ "$(uname -s)" == "Darwin" ]] || return 0
  [[ $nodes -gt 1 ]] || return 0

  log "Настройка loopback-алиасов 127.0.0.2..127.0.0.$nodes (потребуется sudo)..."
  for ((i=2; i<=nodes; i++)); do
    sudo ifconfig lo0 -alias "127.0.0.$i" 2>/dev/null || true
    sudo ifconfig lo0 alias "127.0.0.$i" up
    info "алиас 127.0.0.$i"
  done
}

teardown_loopback_aliases() {
  [[ "$(uname -s)" == "Darwin" ]] || return 0
  # Перебираем все возможные адреса которые мы могли создать
  # (ifconfig покажет только реально привязанные; остальные — no-op).
  local aliases
  aliases=$(ifconfig lo0 2>/dev/null | awk '/inet 127\.0\.0\.[0-9]+ / && $2!="127.0.0.1" {print $2}')
  [[ -n "$aliases" ]] || return 0
  log "Удаление loopback-алиасов (потребуется sudo)..."
  for addr in $aliases; do
    sudo ifconfig lo0 -alias "$addr" 2>/dev/null && info "убран $addr" || true
  done
}

# =============================================================================
# Nomad: multi-node кластер
# =============================================================================
# Каждый агент привязан к своему адресу 127.0.0.N, порты у всех одинаковые
# (4646/4647/4648) — конфликта нет, так как адреса разные.
# Нода i → 127.0.0.i.
start_nomad_cluster() {
  local nodes=$1
  log "Запуск Nomad-кластера: $nodes нод..."

  for ((i=1; i<=nodes; i++)); do
    local data_dir="$NOMAD_DATA_BASE/node$i"
    local log_file="/tmp/platform-dev-nomad-$i.log"
    local addr="127.0.0.$i"

    mkdir -p "$data_dir"

    cat > "$data_dir/agent.hcl" <<HCL
name      = "node-$i"
data_dir  = "$data_dir"
log_level = "INFO"
log_json  = true
bind_addr = "$addr"

advertise {
  http = "$addr:4646"
  rpc  = "$addr:4647"
  serf = "$addr:4648"
}

ports {
  http = 4646
  rpc  = 4647
  serf = 4648
}

server {
  enabled          = true
  bootstrap_expect = $nodes
}

client {
  enabled           = true
  network_interface = "lo0"

  # host_network «node» привязана к конкретному loopback-адресу этой ноды.
  # Порты аллокаций с host_network="node" резолвятся именно в этот адрес,
  # благодаря чему gateway (static=8080) может запускаться на всех нодах
  # одновременно — каждый биндит свой 127.0.0.N:8080.
  host_network "node" {
    cidr = "$addr/32"
  }

  options = { "driver.raw_exec.enable" = "1" }
}
HCL

    local join_flag=""
    if [[ $i -gt 1 ]]; then
      join_flag="-join=127.0.0.1:4648"
    fi

    nomad agent -config="$data_dir/agent.hcl" $join_flag \
      >> "$log_file" 2>&1 &
    local pid=$!
    echo "$pid" >> "$PID_FILE"
    info "Нода $i: $addr:4646 | PID=$pid | Логи: $log_file"
  done

  info "Nomad UI: http://127.0.0.1:4646"
}

# =============================================================================
# Ожидание готовности Nomad
# =============================================================================
wait_nomad() {
  local nodes=$1
  log "Ожидание Nomad (leader election, bootstrap_expect=$nodes)..."
  local max_wait=60
  local elapsed=0
  until nomad node status &>/dev/null; do
    sleep 1
    elapsed=$((elapsed + 1))
    [[ $elapsed -lt $max_wait ]] || die "Nomad не поднялся за ${max_wait}s. Проверьте логи."
  done

  if [[ $nodes -gt 1 ]]; then
    # Дополнительно ждём, пока все ноды зарегистрируются
    until [[ $(nomad node status -short 2>/dev/null | grep -c "ready") -ge $nodes ]]; do
      sleep 2
      elapsed=$((elapsed + 2))
      [[ $elapsed -lt $max_wait ]] || die "Не все ноды готовы за ${max_wait}s."
    done
  fi

  info "Nomad готов"
}

# =============================================================================
# Деплой джобов (dev-режим, бинарники из локального bin/)
#
# Prod job-файлы используют artifact для скачивания из GitHub Releases.
# В dev artifact не нужен — генерируем упрощённые inline-джобы с прямым
# путём к локально собранным бинарникам.
# =============================================================================
deploy_jobs() {
  local bin_dir=$1
  local nodes=$2
  # x-сервисы: count = min(nodes, 3) с distinct_hosts — синхронизировано с prod.
  # gateway type=system, ему copies не нужен.
  local copies=$(( nodes < 3 ? nodes : 3 ))
  log "Деплой джобов (локальные бинарники: $bin_dir, count=$copies)..."

  # Загружаем dev.vars как переменные окружения.
  # Формат файла: key = "value" → конвертируем в key="value" и eval.
  # Совместимо с bash 3.2 (macOS).
  eval "$(grep -v '^\s*#' "$VARS_FILE" | grep -v '^\s*$' | sed 's/[[:space:]]*=[[:space:]]*/=/')"

  # Determining фактический host-порт первого NATS-контейнера.
  # При scale=1 compose публикует 4222:4222 (фиксированно), при scale>1 —
  # диапазон 4222-4322:4222, OrbStack выделяет произвольные значения
  # (4228, 4229, ...). Все Nomad-ноды dev-кластера работают на 127.0.0.1
  # и подключаются к одному и тому же NATS endpoint (кластер реплицирует
  # сам) — этого достаточно для проверки поведения сервисов.
  local nats_host_port
  nats_host_port=$(docker port dev-nats-1 4222/tcp 2>/dev/null | awk -F: '/0\.0\.0\.0:/ {print $NF; exit}')
  [[ -n "$nats_host_port" ]] || die "не удалось определить host-порт NATS"
  info "NATS host-порт: $nats_host_port"

  # Четыре отдельных джоба — точное соответствие prod (gateway/xauth/xhttp/xws.nomad).
  # Отличие от prod: нет блока artifact, бинарник берётся напрямую из bin/.

  cat > /tmp/dev-gateway.nomad << NOMAD
job "gateway" {
  datacenters = ["dc1"]
  # type = "system" — один экземпляр на каждую Nomad-ноду. Синхронизировано
  # с deployments/infra/nomad/gateway.nomad. В multi-node dev работает за счёт
  # loopback-алиасов: каждая нода на своём 127.0.0.N, конфликта на :8080 нет.
  type        = "system"

  update {
    max_parallel      = 1
    min_healthy_time  = "10s"
    healthy_deadline  = "3m"
    progress_deadline = "10m"
    auto_revert       = true
  }

  group "gateway" {
    network {
      port "http" {
        static       = 8080
        host_network = "node"
      }
    }

    restart {
      attempts = 10
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    task "gateway" {
      driver       = "raw_exec"
      kill_timeout = "30s"

      logs {
        max_files     = 5
        max_file_size = 10
      }

      config {
        command = "$bin_dir/gateway"
      }

      env {
        NATS_HOST                = "127.0.0.1"
        NATS_PORT                = "$nats_host_port"
        NATS_USER                = "$nats_user"
        NATS_PASSWORD            = "$nats_password"
        HTTP_ADDR                = "\${NOMAD_IP_http}:8080"
        ALLOWED_HOSTS            = "$allowed_hosts"
        GATEWAY_AUTH_RATE_PREFIX = "$gateway_auth_rate_prefix"
        LOG_LEVEL                = "$log_level"
      }

      service {
        name     = "gateway"
        port     = "http"
        provider = "nomad"

        check {
          name     = "http-health"
          type     = "http"
          path     = "/health"
          interval = "10s"
          timeout  = "3s"
        }
      }

      resources {
        cpu    = 200
        memory = 64
      }
    }
  }
}
NOMAD

  cat > /tmp/dev-xauth.nomad << NOMAD
job "xauth" {
  datacenters = ["dc1"]
  type        = "service"

  update {
    max_parallel     = 1
    min_healthy_time = "10s"
    healthy_deadline = "3m"
    progress_deadline = "10m"
    auto_revert      = true
  }

  group "xauth" {
    count = $copies

    constraint {
      distinct_hosts = true
    }

    network {
      port "health" {}
    }

    restart {
      attempts = 10
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    service {
      name     = "xauth"
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

    task "xauth" {
      driver       = "raw_exec"
      kill_timeout = "30s"

      logs {
        max_files     = 5
        max_file_size = 10
      }

      config {
        command = "$bin_dir/xauth"
      }

      env {
        NATS_HOST           = "127.0.0.1"
        NATS_PORT           = "$nats_host_port"
        NATS_USER           = "$nats_user"
        NATS_PASSWORD       = "$nats_password"
        AUTH_USERNAME       = "$auth_username"
        AUTH_PASSWORD       = "$auth_password"
        AUTH_ACCESS_SECRET  = "$auth_access_secret"
        AUTH_REFRESH_SECRET = "$auth_refresh_secret"
        AUTH_ACCESS_TTL     = "${auth_access_ttl:-15m}"
        AUTH_REFRESH_TTL    = "${auth_refresh_ttl:-168h}"
        COOKIE_DOMAIN       = "$cookie_domain"
        COOKIE_SECURE       = "${cookie_secure:-false}"
        HEALTH_ADDR         = "\${NOMAD_IP_health}:\${NOMAD_PORT_health}"
        LOG_LEVEL           = "$log_level"
      }

      resources {
        cpu    = 100
        memory = 32
      }
    }
  }
}
NOMAD

  cat > /tmp/dev-xhttp.nomad << NOMAD
job "xhttp" {
  datacenters = ["dc1"]
  type        = "service"

  update {
    max_parallel     = 1
    min_healthy_time = "10s"
    healthy_deadline = "3m"
    progress_deadline = "10m"
    auto_revert      = true
  }

  group "xhttp" {
    count = $copies

    constraint {
      distinct_hosts = true
    }

    network {
      port "health" {}
    }

    restart {
      attempts = 10
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    service {
      name     = "xhttp"
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

    task "xhttp" {
      driver       = "raw_exec"
      kill_timeout = "30s"

      logs {
        max_files     = 5
        max_file_size = 10
      }

      config {
        command = "$bin_dir/xhttp"
      }

      env {
        NATS_HOST     = "127.0.0.1"
        NATS_PORT     = "$nats_host_port"
        NATS_USER     = "$nats_user"
        NATS_PASSWORD = "$nats_password"
        DATABASE_URL  = "$database_url"
        ACCESS_SECRET = "$access_secret"
        CACHE_TTL     = "${cache_ttl:-30s}"
        HEALTH_ADDR   = "\${NOMAD_IP_health}:\${NOMAD_PORT_health}"
        LOG_LEVEL     = "$log_level"
      }

      resources {
        cpu    = 100
        memory = 64
      }
    }
  }
}
NOMAD

  cat > /tmp/dev-xws.nomad << NOMAD
job "xws" {
  datacenters = ["dc1"]
  type        = "service"

  update {
    max_parallel     = 1
    min_healthy_time = "10s"
    healthy_deadline = "3m"
    progress_deadline = "10m"
    auto_revert      = true
  }

  group "xws" {
    count = $copies

    constraint {
      distinct_hosts = true
    }

    network {
      port "health" {}
    }

    restart {
      attempts = 10
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    service {
      name     = "xws"
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

    task "xws" {
      driver       = "raw_exec"
      kill_timeout = "30s"

      logs {
        max_files     = 5
        max_file_size = 10
      }

      config {
        command = "$bin_dir/xws"
      }

      env {
        NATS_HOST          = "127.0.0.1"
        NATS_PORT          = "$nats_host_port"
        NATS_USER          = "$nats_user"
        NATS_PASSWORD      = "$nats_password"
        INACTIVITY_TIMEOUT = "${inactivity_timeout:-3m}"
        HEALTH_ADDR        = "\${NOMAD_IP_health}:\${NOMAD_PORT_health}"
        LOG_LEVEL          = "$log_level"
      }

      resources {
        cpu    = 100
        memory = 32
      }
    }
  }
}
NOMAD

  # -detach: nomad job run возвращается сразу после регистрации джоба в Raft,
  # не дожидаясь деплоймента (min_healthy_time=10s × 4 job = 40s минимум в serial).
  # Все 4 сервиса стартуют параллельно — Nomad сам разведёт аллокации по нодам.
  for job in gateway xauth xhttp xws; do
    nomad job run -detach "/tmp/dev-${job}.nomad" >/dev/null
    info "✓ $job submitted"
  done
}

# =============================================================================
# Статус
# =============================================================================
print_status() {
  echo ""
  echo -e "${GREEN}═══════════════════════════════════════${NC}"
  echo -e "${GREEN}  Dev-окружение запущено${NC}"
  echo -e "${GREEN}═══════════════════════════════════════${NC}"
  for job in gateway xauth xhttp xws; do
    nomad job status "$job" 2>/dev/null | grep -E "^(ID|Status)" | head -2 || true
  done
  echo ""
  info "Gateway:    http://localhost:8080"
  info "Nomad UI:   http://localhost:4646"
  info "NATS:       http://localhost:8222"
  echo ""
  info "Остановить: $SCRIPT_DIR/start.sh stop"
}

# =============================================================================
# Очистка осиротевших процессов сервисов и Nomad-executor'ов
#
# Когда Nomad-агент убит через `kill` (ручная симуляция падения ноды) или
# аварийно — его дочерние таски (бинарники сервисов и их executor'ы) остаются
# работать под init. Следующий старт пытается поднять новый gateway, который
# не может занять тот же 127.0.0.N:8080 — уходит в цикл рестарта.
# =============================================================================
cleanup_orphans() {
  local killed=0
  for svc in gateway xauth xhttp xws; do
    pkill -9 -f "$BIN_DIR/$svc" 2>/dev/null && killed=1 || true
  done
  pkill -9 -f 'nomad executor' 2>/dev/null && killed=1 || true
  [[ $killed -eq 1 ]] && info "✓ осиротевшие процессы убраны" || true
}

# =============================================================================
# Остановка
# =============================================================================
stop_all() {
  log "Остановка сервисов..."
  # Nomad джобы
  for job in gateway xauth xhttp xws; do
    nomad job stop "$job" 2>/dev/null && info "✓ job $job остановлен" || true
  done

  # Nomad агенты
  if [[ -f "$PID_FILE" ]]; then
    while IFS= read -r pid; do
      kill "$pid" 2>/dev/null && info "✓ Nomad PID $pid завершён" || true
    done < "$PID_FILE"
    rm -f "$PID_FILE"
  fi

  # Осиротевшие процессы от аварийных завершений
  cleanup_orphans

  # Docker инфраструктура
  stop_infra

  # Временные данные Nomad и рендер NATS-конфига
  rm -rf "$NOMAD_DATA_BASE"
  rm -f "$NATS_CONF_RENDERED"

  # Loopback-алиасы (macOS)
  teardown_loopback_aliases

  log "Остановлено"
}

# =============================================================================
# Точка входа
# =============================================================================
CMD="${1:-1}"

if [[ "$CMD" == "stop" ]]; then
  stop_all
  exit 0
fi

# Число нод — позиционный аргумент, должен быть целым
if ! [[ "$CMD" =~ ^[0-9]+$ ]] || [[ "$CMD" -lt 1 ]]; then
  die "Использование: $0 [NODES|stop]  (NODES ≥ 1, по умолчанию 1)"
fi

NODES="$CMD"

check_deps

# Страховка: если предыдущий запуск был убит kill'ом без stop — могли
# остаться бинарники сервисов и nomad executor'ы под init. Убираем до старта.
cleanup_orphans

if [[ -f "$PID_FILE" ]]; then
  warn "Обнаружено запущенное окружение. Останавливаю перед повторным запуском..."
  stop_all
fi

start_infra "$NODES"
build_binaries

setup_loopback_aliases "$NODES"
start_nomad_cluster "$NODES"

wait_nomad "$NODES"
wait_nats "$NODES"
deploy_jobs "$BIN_DIR" "$NODES"
print_status
