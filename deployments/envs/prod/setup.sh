#!/usr/bin/env bash
# deployments/envs/prod/setup.sh
#
# Полная настройка production-ноды — одна команда, без предположений о порядке.
# Первая нода кластера или двадцать первая — поведение одинаковое.
#
# ─────────────────────────────────────────────────────────────────────────────
# Использование (wget):
#   wget -qO- https://raw.githubusercontent.com/OWNER/REPO/main/deployments/envs/prod/setup.sh \
#     | PLATFORM_DOMAIN=nodes.example.com NATS_USER=nats NATS_PASSWORD=secret bash
#
# Использование (curl):
#   curl -fsSL https://raw.githubusercontent.com/OWNER/REPO/main/deployments/envs/prod/setup.sh \
#     | PLATFORM_DOMAIN=nodes.example.com NATS_USER=nats NATS_PASSWORD=secret bash
# ─────────────────────────────────────────────────────────────────────────────
#
# Обязательные переменные:
#   PLATFORM_DOMAIN  — домен A-записей кластера (все ноды)
#                      например: nodes.example.com
#   NATS_USER        — логин NATS-сервера
#   NATS_PASSWORD    — пароль NATS-сервера
#
# Необязательные:
#   NATS_VERSION     — версия NATS Server    (по умолчанию: 2.10.22)
#   REPO_URL         — URL git-репозитория   (нужен если деплоить джобы с этой ноды)

set -euo pipefail

# =============================================================================
# Переменные
# =============================================================================
: "${PLATFORM_DOMAIN:?Обязательная переменная: PLATFORM_DOMAIN}"
: "${NATS_USER:?Обязательная переменная: NATS_USER}"
: "${NATS_PASSWORD:?Обязательная переменная: NATS_PASSWORD}"

NATS_VERSION="${NATS_VERSION:-2.10.22}"
REPO_URL="${REPO_URL:-}"

PLATFORM_DIR="/opt/platform"
NOMAD_CONF_DIR="/etc/nomad"
NATS_CONF_DIR="/etc/nats"
NOMAD_DATA_DIR="/var/lib/nomad"
NATS_DATA_DIR="/var/lib/nats"

# =============================================================================
# Вывод
# =============================================================================
log()  { printf '▶ %s\n' "$*"; }
info() { printf '  %s\n' "$*"; }
warn() { printf '⚠ %s\n' "$*"; }
die()  { printf '✗ %s\n' "$*" >&2; exit 1; }

# =============================================================================
# Предусловия
# =============================================================================
[[ $EUID -eq 0 ]] || die "Запускайте от root: sudo bash"
command -v apt-get >/dev/null 2>&1 || die "Требуется Ubuntu/Debian"

# =============================================================================
# IP ноды
# Определяем через таблицу маршрутизации; fallback — первый глобальный адрес.
# =============================================================================
detect_node_ip() {
  local ip
  ip=$(ip -4 route get 8.8.8.8 2>/dev/null \
    | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
  if [[ -z "$ip" ]]; then
    ip=$(ip -4 addr show scope global \
      | awk '/inet/ {print $2}' | cut -d/ -f1 | head -1)
  fi
  echo "$ip"
}

NODE_IP=$(detect_node_ip)
[[ -n "$NODE_IP" ]] || die "Не удалось определить IP ноды"
log "IP ноды: $NODE_IP"

# =============================================================================
# Swap
# Размер: 2×RAM, но не более 10% свободного диска и не более 4 GB.
# Если своп уже есть — пропускаем.
# =============================================================================
setup_swap() {
  if swapon --show | grep -q '/swapfile'; then
    info "Swap уже настроен: $(free -h | awk '/^Swap:/ {print $2}')"
    return
  fi

  log "Настройка swap..."

  local ram_mb free_disk_mb swap_mb max_from_disk
  ram_mb=$(free -m | awk '/^Mem:/ {print $2}')
  free_disk_mb=$(df -m / | awk 'NR==2 {print $4}')

  swap_mb=$(( ram_mb * 2 ))
  max_from_disk=$(( free_disk_mb / 10 ))
  [[ $swap_mb -gt $max_from_disk ]] && swap_mb=$max_from_disk
  [[ $swap_mb -gt 4096 ]] && swap_mb=4096
  [[ $swap_mb -lt 256  ]] && swap_mb=256

  fallocate -l "${swap_mb}M" /swapfile
  chmod 600 /swapfile
  mkswap  /swapfile >/dev/null
  swapon  /swapfile
  echo '/swapfile none swap sw 0 0' >> /etc/fstab

  # vm.swappiness=10 — своп только при острой нехватке RAM
  echo 'vm.swappiness=10' > /etc/sysctl.d/99-swap.conf
  sysctl -p /etc/sysctl.d/99-swap.conf >/dev/null

  info "Swap: ${swap_mb} MB  (RAM: ${ram_mb} MB, свободный диск: ${free_disk_mb} MB)"
}

# =============================================================================
# Базовые пакеты
# =============================================================================
install_base() {
  log "Установка базовых пакетов..."
  apt-get update -q
  apt-get install -y -q curl wget git unzip gnupg lsb-release ufw dnsutils
}

# =============================================================================
# Nomad
# =============================================================================
install_nomad() {
  if command -v nomad &>/dev/null; then
    info "Nomad уже установлен: $(nomad version | head -1)"
    return
  fi
  log "Установка Nomad (HashiCorp APT)..."
  wget -qO /usr/share/keyrings/hashicorp-archive-keyring.gpg \
    https://apt.releases.hashicorp.com/gpg
  echo "deb [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] \
https://apt.releases.hashicorp.com $(lsb_release -cs) main" \
    > /etc/apt/sources.list.d/hashicorp.list
  apt-get update -q
  apt-get install -y -q nomad
  info "Установлен: $(nomad version | head -1)"
}

setup_nomad() {
  log "Настройка Nomad..."

  id nomad &>/dev/null || useradd -r -s /bin/false nomad
  mkdir -p "$NOMAD_DATA_DIR" "$NOMAD_CONF_DIR"
  chown nomad:nomad "$NOMAD_DATA_DIR"

  # Конфиг (одинаковый для всех нод).
  # ${PLATFORM_DOMAIN} раскрывается Nomad'ом из переменных окружения при старте.
  # bootstrap_expect = 1: нода сразу готова к работе без ожидания кворума.
  # При наличии других нод в DNS — автоматически входит в существующий кластер.
  cat > "$NOMAD_CONF_DIR/nomad.hcl" << 'HCL'
data_dir  = "/var/lib/nomad"
log_level = "INFO"
log_json  = true
bind_addr = "0.0.0.0"

advertise {
  http = "${attr.unique.network.ip-address}"
  rpc  = "${attr.unique.network.ip-address}"
  serf = "${attr.unique.network.ip-address}"
}

server {
  enabled          = true
  bootstrap_expect = 1

  job_gc_threshold        = "4h"
  eval_gc_threshold       = "4h"
  deployment_gc_threshold = "4h"
  node_gc_threshold       = "24h"
}

# Автоматическое обнаружение кластера через DNS.
# Все ноды добавляют свой IP в A-записи PLATFORM_DOMAIN — Nomad находит их здесь.
server_join {
  retry_join     = ["${PLATFORM_DOMAIN}"]
  retry_max      = 0
  retry_interval = "15s"
}

client {
  enabled = true

  reserved {
    memory = 250  # MB: OS ~150 + Nomad+NATS ~100
    cpu    = 100  # MHz
  }

  options = {
    "driver.raw_exec.enable" = "1"
  }
}

ports {
  http = 4646
  rpc  = 4647
  serf = 4648
}

telemetry {
  publish_allocation_metrics = true
  publish_node_metrics       = true
}
HCL

  # Env-файл для systemd (chmod 600 — только root)
  cat > "$NOMAD_CONF_DIR/env" << ENV
PLATFORM_DOMAIN=${PLATFORM_DOMAIN}
ENV
  chmod 600 "$NOMAD_CONF_DIR/env"

  cat > /etc/systemd/system/nomad.service << 'UNIT'
[Unit]
Description=Nomad Agent
Documentation=https://developer.hashicorp.com/nomad/docs
After=network-online.target nats.service
Wants=network-online.target

[Service]
EnvironmentFile=/etc/nomad/env
ExecStart=/usr/bin/nomad agent -config=/etc/nomad/nomad.hcl
ExecReload=/bin/kill -HUP $MAINPID
KillSignal=SIGINT
KillMode=process
Restart=on-failure
RestartSec=5
LimitNOFILE=65536
TasksMax=infinity

[Install]
WantedBy=multi-user.target
UNIT

  systemctl daemon-reload
  systemctl enable nomad
  info "Nomad настроен"
}

# =============================================================================
# NATS
# =============================================================================
install_nats() {
  if command -v nats-server &>/dev/null; then
    info "NATS уже установлен: $(nats-server --version)"
    return
  fi
  log "Установка NATS Server v${NATS_VERSION}..."
  local arch tarball
  arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
  tarball="nats-server-v${NATS_VERSION}-linux-${arch}.tar.gz"
  wget -qO "/tmp/${tarball}" \
    "https://github.com/nats-io/nats-server/releases/download/v${NATS_VERSION}/${tarball}"
  tar -xzf "/tmp/${tarball}" -C /tmp
  mv "/tmp/nats-server-v${NATS_VERSION}-linux-${arch}/nats-server" /usr/local/bin/nats-server
  chmod +x /usr/local/bin/nats-server
  rm -rf "/tmp/${tarball}" "/tmp/nats-server-v${NATS_VERSION}-linux-${arch}"
  info "Установлен: $(nats-server --version)"
}

setup_nats() {
  log "Настройка NATS..."

  id nats &>/dev/null || useradd -r -s /bin/false nats
  mkdir -p "$NATS_DATA_DIR/jetstream" "$NATS_CONF_DIR"
  chown -R nats:nats "$NATS_DATA_DIR"

  # Конфиг (одинаковый для всех нод).
  # $PLATFORM_DOMAIN и остальные переменные раскрываются NATS'ом из env при старте.
  cat > "$NATS_CONF_DIR/nats.conf" << 'CONF'
port:      4222
http_port: 8222

server_name: $HOSTNAME

cluster {
  name: "platform"
  port: 6222

  # DNS Discovery: NATS резолвит все A-записи домена и строит полную mesh-сеть.
  # Добавление новой ноды = новая A-запись, без правки конфига.
  routes: ["nats-route://$PLATFORM_DOMAIN:6222"]

  cluster_advertise: $NODE_IP
}

jetstream {
  store_dir: "/var/lib/nats/jetstream"
  max_mem:   512M
  max_file:  10G
}

authorization {
  user:     $NATS_CLUSTER_USER
  password: $NATS_CLUSTER_PASSWORD
}
CONF

  # Env-файл для systemd (chmod 600 — только root)
  cat > "$NATS_CONF_DIR/env" << ENV
HOSTNAME=$(hostname)
NODE_IP=${NODE_IP}
PLATFORM_DOMAIN=${PLATFORM_DOMAIN}
NATS_CLUSTER_USER=${NATS_USER}
NATS_CLUSTER_PASSWORD=${NATS_PASSWORD}
ENV
  chmod 600 "$NATS_CONF_DIR/env"

  cat > /etc/systemd/system/nats.service << 'UNIT'
[Unit]
Description=NATS Server
Documentation=https://docs.nats.io
After=network-online.target
Wants=network-online.target

[Service]
User=nats
EnvironmentFile=/etc/nats/env
ExecStart=/usr/local/bin/nats-server -c /etc/nats/nats.conf
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT

  systemctl daemon-reload
  systemctl enable nats
  info "NATS настроен"
}

# =============================================================================
# Репозиторий (опционально — нужен только если запускать nomad job run с этой ноды)
# =============================================================================
clone_repo() {
  [[ -n "$REPO_URL" ]] || return 0
  if [[ -d "$PLATFORM_DIR/.git" ]]; then
    log "Обновление репозитория..."
    git -C "$PLATFORM_DIR" pull
  else
    log "Клонирование репозитория → $PLATFORM_DIR ..."
    git clone "$REPO_URL" "$PLATFORM_DIR"
  fi
}

# =============================================================================
# Firewall
# =============================================================================
setup_firewall() {
  log "Настройка ufw..."
  ufw --force reset
  ufw default deny incoming
  ufw default allow outgoing

  ufw allow 22/tcp   comment 'SSH'
  ufw allow 8080/tcp comment 'Gateway HTTP'

  # Межнодовые порты (ограничьте диапазоном IP нод в production)
  ufw allow 4222/tcp comment 'NATS client'
  ufw allow 6222/tcp comment 'NATS cluster'
  ufw allow 4646/tcp comment 'Nomad HTTP API'
  ufw allow 4647/tcp comment 'Nomad RPC'
  ufw allow 4647/udp comment 'Nomad RPC UDP'
  ufw allow 4648/tcp comment 'Nomad Serf'
  ufw allow 4648/udp comment 'Nomad Serf UDP'

  ufw --force enable
  info "Firewall настроен"
}

# =============================================================================
# Запуск
# =============================================================================
start_services() {
  log "Запуск сервисов..."
  systemctl start nats
  sleep 2
  systemctl start nomad
  info "NATS:  $(systemctl is-active nats)"
  info "Nomad: $(systemctl is-active nomad)"
}

# =============================================================================
# Итог
# =============================================================================
print_summary() {
  printf '\n'
  printf '═══════════════════════════════════════════\n'
  printf '  Нода готова: %s\n' "$NODE_IP"
  printf '═══════════════════════════════════════════\n'
  info "NATS мониторинг: http://${NODE_IP}:8222"
  info "Nomad UI:        http://${NODE_IP}:4646"
  printf '\n'
  warn "A-запись ${PLATFORM_DOMAIN} → ${NODE_IP} должна быть добавлена в DNS"
  warn "После добавления DNS-записи нода автоматически войдёт в кластер"
}

# =============================================================================
# Точка входа
# =============================================================================
setup_swap
install_base
install_nomad
install_nats
clone_repo
setup_nomad
setup_nats
setup_firewall
start_services
print_summary
