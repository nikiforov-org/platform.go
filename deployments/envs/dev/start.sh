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
JOB_PLATFORM="$ROOT_DIR/deployments/infra/nomad/platform.nomad"
JOB_XSERVICES="$ROOT_DIR/deployments/infra/nomad/xservices.nomad"
BIN_DIR="$ROOT_DIR/bin"
NOMAD_DATA_BASE="/tmp/platform-dev"
PID_FILE="/tmp/platform-dev-pids"

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
# Инфраструктура (NATS + PostgreSQL)
# =============================================================================
start_infra() {
  local nodes=$1
  log "Запуск инфраструктуры: $nodes нод NATS + PostgreSQL..."
  docker compose -f "$COMPOSE_FILE" up -d --scale nats="$nodes" --remove-orphans
  info "NATS мониторинг: http://localhost:8222"
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
  for svc in gateway xauth xhttp xws; do
    go build -o "$BIN_DIR/$svc" "./cmd/$svc"
    info "✓ $svc"
  done
}

# =============================================================================
# Nomad: single-node (-dev режим)
# =============================================================================
start_nomad_dev() {
  log "Запуск Nomad (dev, single-node)..."
  nomad agent -dev \
    -bind=127.0.0.1 \
    -log-level=INFO \
    >> /tmp/platform-dev-nomad.log 2>&1 &
  echo "$!" >> "$PID_FILE"
  info "PID $! | Логи: /tmp/platform-dev-nomad.log"
  info "Nomad UI: http://localhost:4646"
}

# =============================================================================
# Nomad: multi-node кластер
# =============================================================================
# Каждый агент работает на 127.0.0.1 с разными портами.
# Порты агента i: http=4646+i*10, rpc=4647+i*10, serf=4648+i*10
#
# На Linux 127.x.x.x работает без настройки.
# На macOS нужны loopback-алиасы (скрипт добавит их через sudo ifconfig).
start_nomad_cluster() {
  local nodes=$1
  log "Запуск Nomad-кластера: $nodes нод..."

  local os
  os="$(uname -s)"

  for ((i=1; i<=nodes; i++)); do
    local data_dir="$NOMAD_DATA_BASE/node$i"
    local log_file="/tmp/platform-dev-nomad-$i.log"
    local http_port=$((4646 + (i - 1) * 10))
    local rpc_port=$((4647  + (i - 1) * 10))
    local serf_port=$((4648  + (i - 1) * 10))

    mkdir -p "$data_dir"

    # Генерируем конфиг ноды
    cat > "$data_dir/agent.hcl" <<HCL
data_dir  = "$data_dir"
log_level = "INFO"
log_json  = true
bind_addr = "127.0.0.1"

advertise {
  http = "127.0.0.1:$http_port"
  rpc  = "127.0.0.1:$rpc_port"
  serf = "127.0.0.1:$serf_port"
}

ports {
  http = $http_port
  rpc  = $rpc_port
  serf = $serf_port
}

server {
  enabled          = true
  bootstrap_expect = $nodes
}

client {
  enabled = true
  options = { "driver.raw_exec.enable" = "1" }
}
HCL

    # Флаг join — все ноды, кроме первой, джойнятся к первой
    local join_flag=""
    if [[ $i -gt 1 ]]; then
      join_flag="-join=127.0.0.1:4648"
    fi

    nomad agent -config="$data_dir/agent.hcl" $join_flag \
      >> "$log_file" 2>&1 &
    local pid=$!
    echo "$pid" >> "$PID_FILE"
    info "Нода $i: http=:$http_port | PID=$pid | Логи: $log_file"
  done

  info "Nomad UI: http://localhost:4646"
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
  log "Деплой джобов (локальные бинарники: $bin_dir)..."

  # Считываем dev.vars в ассоциативный массив для подстановки в джобы
  local -A v=()
  while IFS='=' read -r key val; do
    [[ "$key" =~ ^[[:space:]]*# ]] && continue
    [[ -z "$key" ]] && continue
    key="${key// /}"
    val="${val// /}"
    val="${val//\"/}"
    v["$key"]="$val"
  done < "$VARS_FILE"

  # platform.nomad (Gateway)
  cat > /tmp/platform-dev.nomad << NOMAD
job "platform" {
  datacenters = ["dc1"]
  type        = "service"

  update {
    max_parallel     = 1
    min_healthy_time = "10s"
    healthy_deadline = "3m"
    auto_revert      = true
  }

  group "gateway" {
    count = 1
    network { port "http" { static = 8080 } }
    logs { max_files = 5; max_file_size = 10 }
    restart { attempts = 10; interval = "5m"; delay = "15s"; mode = "delay" }

    task "gateway" {
      driver = "raw_exec"
      config { command = "$bin_dir/gateway" }
      env {
        NATS_HOST                = "127.0.0.1"
        NATS_PORT                = "4222"
        NATS_USER                = "${v[nats_user]}"
        NATS_PASSWORD            = "${v[nats_password]}"
        HTTP_ADDR                = ":8080"
        ALLOWED_HOSTS            = "${v[allowed_hosts]}"
        GATEWAY_AUTH_RATE_PREFIX = "${v[gateway_auth_rate_prefix]}"
        LOG_LEVEL                = "${v[log_level]}"
      }
      service {
        name = "gateway"; port = "http"; provider = "nomad"
        check { name = "http-health"; type = "http"; path = "/health"; interval = "10s"; timeout = "3s" }
      }
      resources { cpu = 200; memory = 64 }
    }
  }
}
NOMAD

  # xservices.nomad (xauth + xhttp + xws)
  cat > /tmp/xservices-dev.nomad << NOMAD
job "xservices" {
  datacenters = ["dc1"]
  type        = "service"

  update {
    max_parallel     = 1
    min_healthy_time = "10s"
    healthy_deadline = "3m"
    auto_revert      = true
  }

  group "xauth" {
    count = 1
    logs { max_files = 5; max_file_size = 10 }
    restart { attempts = 10; interval = "5m"; delay = "15s"; mode = "delay" }
    task "xauth" {
      driver = "raw_exec"
      config { command = "$bin_dir/xauth" }
      env {
        NATS_HOST           = "127.0.0.1"
        NATS_PORT           = "4222"
        NATS_USER           = "${v[nats_user]}"
        NATS_PASSWORD       = "${v[nats_password]}"
        AUTH_USERNAME       = "${v[auth_username]}"
        AUTH_PASSWORD       = "${v[auth_password]}"
        AUTH_ACCESS_SECRET  = "${v[auth_access_secret]}"
        AUTH_REFRESH_SECRET = "${v[auth_refresh_secret]}"
        AUTH_ACCESS_TTL     = "${v[auth_access_ttl]:-15m}"
        AUTH_REFRESH_TTL    = "${v[auth_refresh_ttl]:-168h}"
        COOKIE_DOMAIN       = "${v[cookie_domain]}"
        COOKIE_SECURE       = "${v[cookie_secure]:-false}"
        LOG_LEVEL           = "${v[log_level]}"
      }
      resources { cpu = 100; memory = 32 }
    }
  }

  group "xhttp" {
    count = 1
    logs { max_files = 5; max_file_size = 10 }
    restart { attempts = 10; interval = "5m"; delay = "15s"; mode = "delay" }
    task "xhttp" {
      driver = "raw_exec"
      config { command = "$bin_dir/xhttp" }
      env {
        NATS_HOST     = "127.0.0.1"
        NATS_PORT     = "4222"
        NATS_USER     = "${v[nats_user]}"
        NATS_PASSWORD = "${v[nats_password]}"
        DATABASE_URL  = "${v[database_url]}"
        ACCESS_SECRET = "${v[access_secret]}"
        CACHE_TTL     = "${v[cache_ttl]:-30s}"
        LOG_LEVEL     = "${v[log_level]}"
      }
      resources { cpu = 100; memory = 64 }
    }
  }

  group "xws" {
    count = 1
    logs { max_files = 5; max_file_size = 10 }
    restart { attempts = 10; interval = "5m"; delay = "15s"; mode = "delay" }
    task "xws" {
      driver = "raw_exec"
      config { command = "$bin_dir/xws" }
      env {
        NATS_HOST          = "127.0.0.1"
        NATS_PORT          = "4222"
        NATS_USER          = "${v[nats_user]}"
        NATS_PASSWORD      = "${v[nats_password]}"
        ACCESS_SECRET      = "${v[access_secret]}"
        INACTIVITY_TIMEOUT = "${v[inactivity_timeout]:-3m}"
        LOG_LEVEL          = "${v[log_level]}"
      }
      resources { cpu = 100; memory = 32 }
    }
  }
}
NOMAD

  nomad job run /tmp/platform-dev.nomad
  nomad job run /tmp/xservices-dev.nomad
  info "Джобы задеплоены"
}

# =============================================================================
# Статус
# =============================================================================
print_status() {
  echo ""
  echo -e "${GREEN}═══════════════════════════════════════${NC}"
  echo -e "${GREEN}  Dev-окружение запущено${NC}"
  echo -e "${GREEN}═══════════════════════════════════════${NC}"
  nomad job status platform  2>/dev/null | grep -E "^(ID|Status|)"    | head -4 || true
  nomad job status xservices 2>/dev/null | grep -E "^(ID|Status|)"    | head -4 || true
  echo ""
  info "Gateway:    http://localhost:8080"
  info "Nomad UI:   http://localhost:4646"
  info "NATS:       http://localhost:8222"
  echo ""
  info "Остановить: $SCRIPT_DIR/start.sh stop"
}

# =============================================================================
# Остановка
# =============================================================================
stop_all() {
  log "Остановка сервисов..."
  # Nomad джобы
  nomad job stop platform   2>/dev/null && info "✓ job platform остановлен"   || true
  nomad job stop xservices  2>/dev/null && info "✓ job xservices остановлен"  || true

  # Nomad агенты
  if [[ -f "$PID_FILE" ]]; then
    while IFS= read -r pid; do
      kill "$pid" 2>/dev/null && info "✓ Nomad PID $pid завершён" || true
    done < "$PID_FILE"
    rm -f "$PID_FILE"
  fi

  # Docker инфраструктура
  stop_infra

  # Временные данные Nomad
  rm -rf "$NOMAD_DATA_BASE"
  log "Остановлено."
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
rm -f "$PID_FILE"

start_infra "$NODES"
build_binaries

if [[ "$NODES" -eq 1 ]]; then
  start_nomad_dev
else
  start_nomad_cluster "$NODES"
fi

wait_nomad "$NODES"
deploy_jobs "$BIN_DIR"
print_status
