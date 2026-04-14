#!/usr/bin/env bash
# deployments/mode/dev/start.sh
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
JOB_PLATFORM="$ROOT_DIR/deployments/services/nomad/platform.nomad"
JOB_XSERVICES="$ROOT_DIR/deployments/services/nomad/xservices.nomad"
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
# Деплой джобов
# =============================================================================
deploy_jobs() {
  local binary_dir=$1
  log "Деплой джобов..."
  nomad job run \
    -var-file="$VARS_FILE" \
    -var="binary_dir=$binary_dir" \
    "$JOB_PLATFORM"
  nomad job run \
    -var-file="$VARS_FILE" \
    -var="binary_dir=$binary_dir" \
    "$JOB_XSERVICES"
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
