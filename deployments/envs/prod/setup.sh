#!/usr/bin/env bash
# deployments/envs/prod/setup.sh
#
# Полная настройка production-ноды — одна команда, без предположений о порядке.
# Первая нода кластера или двадцать первая — поведение одинаковое.
#
# ─────────────────────────────────────────────────────────────────────────────
# Использование (wget):
#   wget -qO- https://raw.githubusercontent.com/OWNER/REPO/main/deployments/envs/prod/setup.sh \
#     | PLATFORM_DOMAIN=nodes.example.com \
#       NATS_USER=nats \
#       NATS_PASSWORD=secret \
#       NATS_CA_KEY="$(base64 -w0 < nats-ca.key)" \
#       NATS_CA_CERT="$(base64 -w0 < nats-ca.crt)" \
#       bash
# ─────────────────────────────────────────────────────────────────────────────
#
# Обязательные переменные:
#   PLATFORM_DOMAIN  — домен A-записей кластера (все ноды), например: nodes.example.com
#   NATS_USER        — логин NATS-сервера
#   NATS_PASSWORD    — пароль NATS-сервера
#   NATS_CA_KEY      — приватный ключ CA в base64 (base64 -w0 < nats-ca.key)
#   NATS_CA_CERT     — сертификат CA в base64   (base64 -w0 < nats-ca.crt)
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
: "${NATS_CA_KEY:?Обязательная переменная: NATS_CA_KEY (base64 приватного ключа CA)}"
: "${NATS_CA_CERT:?Обязательная переменная: NATS_CA_CERT (base64 сертификата CA)}"
: "${NOMAD_TOKEN:?Обязательная переменная: NOMAD_TOKEN (UUID для Nomad ACL)}"

# PEM-ключи передаются через base64 чтобы не ломать env-файл многострочным содержимым.
# Декодируем один раз здесь; дальше используем как обычные переменные.
NATS_CA_KEY=$(printf '%s' "$NATS_CA_KEY" | base64 -d)
NATS_CA_CERT=$(printf '%s' "$NATS_CA_CERT" | base64 -d)

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
  # ВНИМАНИЕ: первичное развёртывание 2+ нод параллельно — окно риска split-brain
  # (DNS не пропагирован, каждая бутстрапится как самостоятельный лидер).
  # Ноды первого кластера поднимать последовательно — см. prod.md «Важно».
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

acl {
  enabled = true
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
  local arch tarball base expected
  arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
  tarball="nats-server-v${NATS_VERSION}-linux-${arch}.tar.gz"
  base="https://github.com/nats-io/nats-server/releases/download/v${NATS_VERSION}"

  # curl -fsSL: -f (fail on 4xx/5xx), -s (silent), -S (show errors), -L (follow redirects).
  # Curl универсальнее wget — присутствует в большинстве минимальных образов,
  # включая cloud-init минимальные Ubuntu / Debian / Alpine.
  curl -fsSL -o "/tmp/${tarball}" "${base}/${tarball}"

  # Сверка SHA256 против SHA256SUMS из того же релиза.
  # Защищает от passive corruption (CDN bitrot, transient transport errors).
  # От активного MITM через github.com TLS не защищает — общий канал — но это
  # за пределами реалистичного threat model для одноразового bootstrap-окна.
  expected=$(curl -fsSL "${base}/SHA256SUMS" \
    | awk -v t="${tarball}" '$2==t {print $1; exit}')
  [[ -n "$expected" ]] || die "SHA256SUMS не содержит строки для ${tarball}"
  echo "${expected}  /tmp/${tarball}" | sha256sum -c - >/dev/null \
    || die "SHA256 mismatch для ${tarball} — файл повреждён или подменён"

  tar -xzf "/tmp/${tarball}" -C /tmp
  mv "/tmp/nats-server-v${NATS_VERSION}-linux-${arch}/nats-server" /usr/local/bin/nats-server
  chmod +x /usr/local/bin/nats-server
  rm -rf "/tmp/${tarball}" "/tmp/nats-server-v${NATS_VERSION}-linux-${arch}"
  info "Установлен: $(nats-server --version)"
}

# =============================================================================
# TLS-сертификаты NATS
#
# CA-ключ (NATS_CA_KEY) получается из env, используется только для подписи
# сертификата этой ноды и сразу удаляется. На сервере остаётся только:
#   /etc/nats/ca.crt   — публичный сертификат CA (для проверки других нод)
#   /etc/nats/node.crt — сертификат этой ноды (подписан CA)
#   /etc/nats/node.key — приватный ключ ноды (chmod 600, owner nats)
# =============================================================================
generate_nats_certs() {
  log "Генерация TLS-сертификатов NATS..."

  command -v openssl >/dev/null || apt-get install -y -q openssl

  # CA cert — публичный, нужен для проверки сертификатов других нод
  printf '%s\n' "$NATS_CA_CERT" > "$NATS_CONF_DIR/ca.crt"
  chmod 644 "$NATS_CONF_DIR/ca.crt"

  # CA key — только для подписи, удаляем сразу после
  printf '%s\n' "$NATS_CA_KEY" > /tmp/nats-ca.key
  chmod 600 /tmp/nats-ca.key

  # Ключ ноды: ECDSA P-256 — NIST-current, защита до 2050+, ~3× быстрее
  # TLS-handshake чем RSA-2048. Алгоритм node-key независим от алгоритма CA-key
  # (NATS_CA_KEY приходит из env, не трогаем).
  openssl ecparam -name prime256v1 -genkey -noout -out "$NATS_CONF_DIR/node.key" 2>/dev/null
  chmod 600 "$NATS_CONF_DIR/node.key"

  # CSR
  openssl req -new \
    -key "$NATS_CONF_DIR/node.key" \
    -out /tmp/nats-node.csr \
    -subj "/CN=nats-${NODE_IP}/O=platform" \
    2>/dev/null

  # SAN: IP ноды + localhost (для локальных health-check'ов)
  printf 'subjectAltName=IP:%s,IP:127.0.0.1\n' "$NODE_IP" > /tmp/nats-node-san.cnf

  # Подписываем сертификат ноды CA-ключом (10 лет)
  openssl x509 -req \
    -in /tmp/nats-node.csr \
    -CA "$NATS_CONF_DIR/ca.crt" \
    -CAkey /tmp/nats-ca.key \
    -CAcreateserial \
    -out "$NATS_CONF_DIR/node.crt" \
    -days 3650 \
    -extfile /tmp/nats-node-san.cnf \
    2>/dev/null

  chmod 644 "$NATS_CONF_DIR/node.crt"

  # CA key немедленно удаляется с сервера
  rm -f /tmp/nats-ca.key /tmp/nats-node.csr /tmp/nats-node-san.cnf /tmp/nats-ca.srl

  chown nats:nats "$NATS_CONF_DIR/ca.crt" "$NATS_CONF_DIR/node.crt" "$NATS_CONF_DIR/node.key"

  local expires
  expires=$(openssl x509 -noout -enddate -in "$NATS_CONF_DIR/node.crt" | cut -d= -f2)
  info "Сертификат ноды действителен до: $expires"
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

  # mTLS для кластерного трафика между нодами (разные DC/провайдеры).
  # CA-ключ не хранится на сервере — только cert + key ноды.
  tls {
    cert_file: "/etc/nats/node.crt"
    key_file:  "/etc/nats/node.key"
    ca_file:   "/etc/nats/ca.crt"
    verify:    true
    timeout:   5
  }
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
# Nomad ACL bootstrap
#
# NOMAD_TOKEN — UUID, сгенерированный заранее и положенный в GitHub Secrets.
# Передаётся в Nomad через HTTP API как BootstrapSecret.
# На первой ноде: Nomad принимает токен и активирует ACL.
# На последующих: запрос вернёт ошибку "bootstrap already done" — игнорируем.
# =============================================================================
bootstrap_acl() {
  log "Настройка Nomad ACL..."

  # Ждём готовности Nomad API
  local elapsed=0
  until curl -sf --max-time 2 "http://127.0.0.1:4646/v1/status/leader" &>/dev/null; do
    sleep 2; elapsed=$((elapsed + 2))
    [[ $elapsed -lt 30 ]] || die "Nomad API не отвечает за 30s"
  done

  # Отправляем bootstrap-токен. Nomad принимает его только один раз —
  # при повторном вызове (на нодах 2+) возвращает ошибку "already bootstrapped" — игнорируем.
  curl -sf -X POST "http://127.0.0.1:4646/v1/acl/bootstrap" \
    -d "{\"BootstrapSecret\": \"${NOMAD_TOKEN}\"}" &>/dev/null || true

  # Проверяем что токен действительно работает — независимо от результата bootstrap выше.
  # Покрывает все сценарии: первая нода, повторный запуск, нода 2+.
  if ! curl -sf --max-time 5 \
      -H "X-Nomad-Token: ${NOMAD_TOKEN}" \
      "http://127.0.0.1:4646/v1/acl/self" &>/dev/null; then
    die "NOMAD_TOKEN не принят Nomad ACL — проверьте что токен совпадает с GitHub Secret NOMAD_TOKEN"
  fi

  info "ACL настроен, токен валиден"
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
generate_nats_certs
setup_nats
setup_firewall
start_services
bootstrap_acl
print_summary
