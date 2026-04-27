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
#       NOMAD_CA_KEY="$(base64 -w0 < nomad-ca.key)" \
#       NOMAD_CA_CERT="$(base64 -w0 < nomad-ca.crt)" \
#       NOMAD_GOSSIP_KEY="$(openssl rand -base64 32)" \
#       NOMAD_TOKEN=$(uuidgen) \
#       bash
# ─────────────────────────────────────────────────────────────────────────────
#
# Обязательные переменные:
#   PLATFORM_DOMAIN  — домен A-записей кластера (все ноды), например: nodes.example.com
#   NATS_USER        — логин NATS-сервера
#   NATS_PASSWORD    — пароль NATS-сервера
#   NATS_CA_KEY      — приватный ключ CA NATS  в base64 (base64 -w0 < nats-ca.key)
#   NATS_CA_CERT     — сертификат CA NATS      в base64 (base64 -w0 < nats-ca.crt)
#   NOMAD_CA_KEY     — приватный ключ CA Nomad в base64 (base64 -w0 < nomad-ca.key)
#   NOMAD_CA_CERT    — сертификат CA Nomad     в base64 (base64 -w0 < nomad-ca.crt)
#   NOMAD_GOSSIP_KEY — gossip-key Nomad в base64 (32 байта; openssl rand -base64 32).
#                      Шифрует Serf-протокол (4648) — у Nomad собственное
#                      симметричное шифрование, через TLS Serf не работает.
#   NOMAD_TOKEN      — UUID для Nomad ACL bootstrap (uuidgen)
#
# Необязательные:
#   NATS_VERSION     — версия NATS Server    (по умолчанию: 2.10.22)
#   REPO_URL         — URL git-репозитория   (нужен если деплоить джобы с этой ноды)

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
   echo "ОШИБКА: Скрипт запущен НЕ под рутом (ваш UID: $EUID)"
   exit 1
fi

# =============================================================================
# Переменные
# =============================================================================
: "${PLATFORM_DOMAIN:?Обязательная переменная: PLATFORM_DOMAIN}"
: "${NATS_USER:?Обязательная переменная: NATS_USER}"
: "${NATS_PASSWORD:?Обязательная переменная: NATS_PASSWORD}"
: "${NATS_CA_KEY:?Обязательная переменная: NATS_CA_KEY (base64 приватного ключа CA NATS)}"
: "${NATS_CA_CERT:?Обязательная переменная: NATS_CA_CERT (base64 сертификата CA NATS)}"
: "${NOMAD_CA_KEY:?Обязательная переменная: NOMAD_CA_KEY (base64 приватного ключа CA Nomad)}"
: "${NOMAD_CA_CERT:?Обязательная переменная: NOMAD_CA_CERT (base64 сертификата CA Nomad)}"
: "${NOMAD_GOSSIP_KEY:?Обязательная переменная: NOMAD_GOSSIP_KEY (32 байта в base64; openssl rand -base64 32)}"
: "${NOMAD_TOKEN:?Обязательная переменная: NOMAD_TOKEN (UUID для Nomad ACL)}"

# PEM-ключи передаются через base64 чтобы не ломать env-файл многострочным содержимым.
# Декодируем один раз здесь; дальше используем как обычные переменные.
# NOMAD_GOSSIP_KEY оставляем как есть — Nomad сам ожидает base64 в server.encrypt.
NATS_CA_KEY=$(printf '%s' "$NATS_CA_KEY" | base64 -d)
NATS_CA_CERT=$(printf '%s' "$NATS_CA_CERT" | base64 -d)
NOMAD_CA_KEY=$(printf '%s' "$NOMAD_CA_KEY" | base64 -d)
NOMAD_CA_CERT=$(printf '%s' "$NOMAD_CA_CERT" | base64 -d)

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
  log "Очистка и обновление кэша пакетов..."
  
  # Универсальное удаление проблемного backports из всех списков
  # Это сработает и на Debian, и на Ubuntu, независимо от зеркала
  sudo sed -i '/backports/d' /etc/apt/sources.list /etc/apt/sources.list.d/* 2>/dev/null || true
  
  # Обновляемся. --allow-releaseinfo-change нужен, если дистрибутив сменил статус.
  # || true гарантирует, что скрипт не упадет, если какое-то зеркало просто "тупит".
  sudo apt-get update -y -q --allow-releaseinfo-change || warn "Некоторые зеркала недоступны, продолжаем..."

  log "Установка базовых пакетов..."
  # Теперь установка curl и прочего пройдет успешно, так как apt больше не блокирует процесс
  sudo apt-get install -y -q --no-install-recommends \
    curl wget git unzip gnupg lsb-release ufw dnsutils
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

  # 1. Чистим старье
  rm -f /usr/share/keyrings/hashicorp-archive-keyring.gpg /etc/apt/sources.list.d/hashicorp.list

  # 2. Качаем ключ с проверкой. 
  # Добавляем -L (follow redirects) и заменяем wget на curl для теста
  if ! curl -fsSL https://apt.releases.hashicorp.com/gpg -o /tmp/hashicorp.gpg; then
     warn "Curl не смог скачать ключ, пробуем wget..."
     wget -qO /tmp/hashicorp.gpg https://apt.releases.hashicorp.com/gpg || die "404: Ключ HashiCorp недоступен. Проверь интернет на ноде: ping google.com"
  fi

  # 3. Деарморим (флаг --batch критичен для CI)
  cat /tmp/hashicorp.gpg | gpg --batch --dearmor -o /usr/share/keyrings/hashicorp-archive-keyring.gpg
  rm -f /tmp/hashicorp.gpg

  # 4. Добавляем репозиторий. 
  # ВНИМАНИЕ: Проверь, чтобы эта строка в твоем редакторе была ОДНОЙ строкой без разрывов
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] https://apt.releases.hashicorp.com $(lsb_release -cs) main" \
    | tee /etc/apt/sources.list.d/hashicorp.list

  # 5. Обновление и установка
  # Оставляем || true, чтобы битые зеркала Selectel не мешали
  apt-get update -y || true
  apt-get install -y nomad

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

# HTTP API (4646) слушает только localhost: операции выполняются через SSH +
# nomad CLI с самой ноды (CI делает то же — SSH→nomad job run). UI / внешний
# мониторинг — через ssh-tunnel: ssh -L 4646:127.0.0.1:4646 user@node.
# RPC (4647) и Serf (4648) остаются на 0.0.0.0 — нужны для inter-node трафика.
# Защищает от случайного открытия порта 4646 наружу при откате ACL/misconfig.
addresses {
  http = "127.0.0.1"
}

advertise {
  http = "${attr.unique.network.ip-address}"
  rpc  = "${attr.unique.network.ip-address}"
  serf = "${attr.unique.network.ip-address}"
}

server {
  enabled          = true
  bootstrap_expect = 1

  # Шифрование Serf (4648) — задаётся CLI-флагом nomad agent -encrypt=...
  # в systemd-юните ниже. В HCL прописать нельзя: Nomad не раскрывает
  # env-переменные в server.encrypt (только в retry_join). Хранить ключ
  # как литерал в /etc/nomad/nomad.hcl (chmod 644) — слабее защита, чем
  # в /etc/nomad/env (chmod 600). См. I-H8.

  # raft_multiplier=5 — масштабирует Raft-таймауты ×5 (heartbeat 1s→5s,
  # election 1s→5s, leader_lease 0.5s→2.5s). Платформа multi-DC (cross-DC
  # latency ≥50ms, transient packet loss); default=1 (LAN <10ms) флапает
  # на WAN-jitter. Trade-off: failover 5-25s вместо 1-5s — приемлемо.
  raft_multiplier = 5

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

# TLS для inter-node RPC (4647). Через RPC идут Raft-консенсус и job specs,
# а в job specs — реальные секреты (NATS_PASSWORD, AUTH_ACCESS_SECRET и др.
# через NOMAD_VAR_*). Без TLS на cross-DC через WAN всё это идёт в открытом
# виде — атакующий с MITM собирает секреты непрерывно. См. I-H8.
#
# http = false (default): HTTP API на 127.0.0.1, в threat model не входит;
# локальные curl/nomad CLI остаются на plain HTTP без cert env vars
# (ACL bootstrap, healthcheck wait-loop, deploy-action probe).
#
# verify_server_hostname = true: при исходящем RPC Nomad проверяет, что
# cert пира содержит SAN server.global.nomad (region=global default).
# CA-ключ хранится только в GitHub Secret NOMAD_CA_KEY; на сервере
# остаются только публичный ca.crt + node.crt + node.key.
tls {
  rpc                    = true
  verify_server_hostname = true

  ca_file   = "/etc/nomad/ca.crt"
  cert_file = "/etc/nomad/node.crt"
  key_file  = "/etc/nomad/node.key"
}
HCL

  # Env-файл для systemd (chmod 600 — только root)
  cat > "$NOMAD_CONF_DIR/env" << ENV
PLATFORM_DOMAIN=${PLATFORM_DOMAIN}
NOMAD_GOSSIP_KEY=${NOMAD_GOSSIP_KEY}
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
# -encrypt: gossip-key для Serf (4648). Передаём через CLI-флаг, чтобы ключ
# жил в /etc/nomad/env (chmod 600), а не в /etc/nomad/nomad.hcl (chmod 644).
# systemd раскрывает ${NOMAD_GOSSIP_KEY} из EnvironmentFile.
ExecStart=/usr/bin/nomad agent -config=/etc/nomad/nomad.hcl -encrypt=${NOMAD_GOSSIP_KEY}
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
  local needs_restart=0
  if command -v nats-server &>/dev/null; then
    local current
    current=$(nats-server --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
    if [[ "$current" == "$NATS_VERSION" ]]; then
      info "NATS уже установлен в нужной версии: v${current}"
      return
    fi
    log "NATS v${current} отличается от ожидаемой v${NATS_VERSION} — переустановка..."
    needs_restart=1
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

  # Если апгрейд на работающей ноде — перезапустить, чтобы systemd подхватил
  # новый бинарник (старый image остаётся в памяти процесса до restart).
  # При первой установке systemd-unit ещё не создан (создаётся в setup_nats),
  # is-active вернёт false → restart пропускается; запуск в start_services.
  if (( needs_restart )) && systemctl is-active nats &>/dev/null; then
    log "Перезапуск nats для подхвата нового бинарника..."
    systemctl restart nats
  fi
  info "Установлен: $(nats-server --version)"
}

# =============================================================================
# TLS-сертификаты Nomad
#
# Симметрично с NATS: CA-ключ из env, используется только для подписи cert
# этой ноды и не пишется на ФС (process substitution в openssl x509 -CAkey).
# На сервере остаётся только:
#   /etc/nomad/ca.crt   — публичный сертификат CA (для проверки других нод)
#   /etc/nomad/node.crt — сертификат этой ноды (подписан CA)
#   /etc/nomad/node.key — приватный ключ ноды (chmod 600, owner nomad)
#
# SAN node-cert: server.global.nomad + client.global.nomad + IP ноды + 127.0.0.1.
# DNS-имена обязательны для verify_server_hostname=true в nomad.hcl: Nomad
# валидирует, что cert пира содержит именно эти SAN (region=global default).
# При смене region в nomad.hcl — менять и SAN здесь.
# =============================================================================
generate_nomad_certs() {
  log "Генерация TLS-сертификатов Nomad..."

  command -v openssl >/dev/null || apt-get install -y -q --no-install-recommends openssl

  printf '%s\n' "$NOMAD_CA_CERT" > "$NOMAD_CONF_DIR/ca.crt"
  chmod 644 "$NOMAD_CONF_DIR/ca.crt"

  # ECDSA P-256 — соответствует выбору для NATS-cert.
  openssl ecparam -name prime256v1 -genkey -noout -out "$NOMAD_CONF_DIR/node.key" 2>/dev/null
  chmod 600 "$NOMAD_CONF_DIR/node.key"

  openssl req -new \
    -key "$NOMAD_CONF_DIR/node.key" \
    -out /tmp/nomad-node.csr \
    -subj "/CN=server.global.nomad/O=platform" \
    2>/dev/null

  printf 'subjectAltName=DNS:server.global.nomad,DNS:client.global.nomad,IP:%s,IP:127.0.0.1\n' \
    "$NODE_IP" > /tmp/nomad-node-san.cnf

  # Подписываем 10 лет; CA-key через process substitution — на ФС не пишется.
  openssl x509 -req \
    -in /tmp/nomad-node.csr \
    -CA "$NOMAD_CONF_DIR/ca.crt" \
    -CAkey <(printf '%s\n' "$NOMAD_CA_KEY") \
    -CAcreateserial \
    -out "$NOMAD_CONF_DIR/node.crt" \
    -days 3650 \
    -extfile /tmp/nomad-node-san.cnf \
    2>/dev/null

  chmod 644 "$NOMAD_CONF_DIR/node.crt"
  rm -f /tmp/nomad-node.csr /tmp/nomad-node-san.cnf /tmp/nomad-ca.srl

  chown nomad:nomad "$NOMAD_CONF_DIR/ca.crt" "$NOMAD_CONF_DIR/node.crt" "$NOMAD_CONF_DIR/node.key"

  local expires
  expires=$(openssl x509 -noout -enddate -in "$NOMAD_CONF_DIR/node.crt" | cut -d= -f2)
  info "Сертификат ноды действителен до: $expires"
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

  command -v openssl >/dev/null || apt-get install -y -q --no-install-recommends openssl

  # CA cert — публичный, нужен для проверки сертификатов других нод
  printf '%s\n' "$NATS_CA_CERT" > "$NATS_CONF_DIR/ca.crt"
  chmod 644 "$NATS_CONF_DIR/ca.crt"

  # CA key используется только при подписи node.crt ниже — передаём через
  # process substitution (<(...)), чтобы ключ ни на один момент не попадал
  # на ФС. См. openssl x509 -CAkey ниже.

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

  # Подписываем сертификат ноды CA-ключом (10 лет).
  # CA-ключ передаём через bash process substitution: openssl получает путь
  # вида /dev/fd/N, ключ живёт только в памяти bash-процесса и пайпе, на
  # диск не пишется ни на один момент.
  openssl x509 -req \
    -in /tmp/nats-node.csr \
    -CA "$NATS_CONF_DIR/ca.crt" \
    -CAkey <(printf '%s\n' "$NATS_CA_KEY") \
    -CAcreateserial \
    -out "$NATS_CONF_DIR/node.crt" \
    -days 3650 \
    -extfile /tmp/nats-node-san.cnf \
    2>/dev/null

  chmod 644 "$NATS_CONF_DIR/node.crt"

  # Промежуточные файлы (CSR, SAN-конфиг, CA-serial). CA-ключ на диск не
  # писался — см. process substitution выше.
  rm -f /tmp/nats-node.csr /tmp/nats-node-san.cnf /tmp/nats-ca.srl

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
port: 4222

# Мониторинг — loopback-only (внешний доступ через SSH-tunnel).
# Defense-in-depth: bind на 127.0.0.1 — первичный слой; UFW — вторичный.
http: "127.0.0.1:8222"

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

  # cluster.authorization { user/password } сознательно не используется:
  # cluster trust полностью основан на mTLS (verify=true) + off-server CA-key.
  # Подробнее о threat model — prod.md → «Безопасность кластера NATS».
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

  # Межнодовые порты (ограничьте диапазоном IP нод в production).
  # Nomad HTTP API (4646) слушает только на 127.0.0.1 (см. nomad.hcl/addresses) —
  # отдельное UFW-правило не требуется и было бы dead-code (kernel отвергнет
  # внешний пакет независимо от UFW). Между нодами Nomad общается через
  # 4647/4648 (RPC/Serf), не 4646.
  ufw allow 4222/tcp comment 'NATS client'
  ufw allow 6222/tcp comment 'NATS cluster'
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

  # Ждём готовности NATS перед запуском Nomad. systemctl is-active вернётся
  # сразу после fork — до открытия портов и до завершения JetStream recovery.
  # /healthz отдаёт 200 только после того, как stream/KV-store восстановлены и
  # сервер готов принимать запросы. Таймаут 60s рассчитан на тяжёлый recovery
  # (большой /var/lib/nats/jetstream).
  # Адрес 127.0.0.1:8222 = `http:` в deployments/infra/nats/nats.conf. При
  # смене там — синхронизировать здесь (та же convention, что для 4222/6222/8080).
  log "Ожидание готовности NATS (JetStream recovery)..."
  local elapsed=0
  until curl -sf --max-time 2 "http://127.0.0.1:8222/healthz" &>/dev/null; do
    sleep 1; elapsed=$((elapsed + 1))
    [[ $elapsed -lt 60 ]] || die "NATS /healthz не отвечает за 60s"
  done

  systemctl start nomad
  info "NATS:  $(systemctl is-active nats) (готов через ${elapsed}s)"
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
  info "NATS мониторинг: ssh -L 8222:127.0.0.1:8222 user@${NODE_IP} → http://localhost:8222"
  info "Nomad UI:        ssh -L 4646:127.0.0.1:4646 user@${NODE_IP} → http://localhost:4646"
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
generate_nomad_certs
generate_nats_certs
setup_nats
setup_firewall
start_services
bootstrap_acl
print_summary
