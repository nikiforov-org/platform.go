# deployments/infra/nomad/nomad.hcl
#
# Конфигурация Nomad-агента в гибридном режиме (server + client).
# Один файл для всех нод.
#
# Переменные окружения (задаются в /etc/nomad/env, раскрываются Nomad при старте):
#   PLATFORM_DOMAIN  — домен A-записей кластера (все ноды)
#   NOMAD_GOSSIP_KEY — 32-байтный gossip-key в base64 для шифрования Serf (4648).
#                       Одинаковый для всех нод; см. server.encrypt ниже.
#
# bootstrap_expect = 1: нода сразу готова к работе.
# server_join retry_join: при наличии других нод в DNS автоматически входит
# в существующий кластер; при отсутствии — работает как single-node кластер.

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

  # bootstrap_expect = 1 на каждой ноде — поддерживаемый Nomad Autopilot паттерн.
  # Каждая нода самостоятельно бутстрапится, затем retry_join объединяет их
  # в единый кластер через Raft-консенсус (лидер выбирается автоматически).
  # Ноды можно добавлять в любом порядке и в любое время.
  #
  # ВНИМАНИЕ: при первичном развёртывании 2+ нод одновременно (DNS ещё не
  # пропагировался, ноды не видят друг друга) каждая забутстрапится как
  # самостоятельный лидер — Autopilot позже сольёт их, но Job state, записанный
  # в этом окне, может быть потерян. Порядок развёртывания первого кластера —
  # последовательный (см. deployments/envs/prod/prod.md).
  bootstrap_expect = 1

  # Шифрование Serf-протокола (4648) — задаётся CLI-флагом nomad agent
  # -encrypt=${NOMAD_GOSSIP_KEY} в systemd-юните (см. setup.sh). В HCL
  # прописать нельзя: Nomad не раскрывает env-переменные в server.encrypt
  # (только в retry_join). Хранить ключ литералом в этом файле небезопасно
  # (chmod 644 < chmod 600 для /etc/nomad/env). Без шифрования: топология
  # кластера (ноды, leadership, статусы) идёт между ДЦ в открытом виде.
  # См. I-H8.

  # raft_multiplier=5 — масштабирует все Raft-таймауты ×5 (heartbeat 1s→5s,
  # election 1s→5s, leader_lease 0.5s→2.5s). Платформа ориентирована на
  # multi-DC-развёртывание (ноды могут жить в разных дата-центрах/облаках),
  # где cross-DC latency ≥50ms и transient packet loss — норма. Default=1
  # рассчитан на LAN (<10ms) и при WAN-jitter вызывает re-election storms.
  # Trade-off: leader failover 5-25s вместо 1-5s — приемлемо, платформа не
  # realtime; стабильность кластера важнее скорости восстановления.
  # Один и тот же конфиг работает в single-DC и multi-DC без изменений.
  raft_multiplier = 5

  job_gc_threshold        = "4h"
  eval_gc_threshold       = "4h"
  deployment_gc_threshold = "4h"
  node_gc_threshold       = "24h"
}

# Автообнаружение кластера через DNS.
# Все ноды имеют A-записи PLATFORM_DOMAIN — Nomad находит их здесь.
server_join {
  retry_join     = ["${PLATFORM_DOMAIN}"]
  retry_max      = 0
  retry_interval = "15s"
}

client {
  enabled = true

  # Резервируем ресурсы ОС и системных демонов (Nomad + NATS ~100 MB).
  reserved {
    memory = 250  # MB: 150 (OS) + 100 (Nomad + NATS)
    cpu    = 100  # MHz
  }

  options = {
    "driver.raw_exec.enable" = "1"
  }
}

ports {
  http = 4646  # HTTP API
  rpc  = 4647  # RPC
  serf = 4648  # Serf gossip
}

telemetry {
  publish_allocation_metrics = true
  publish_node_metrics       = true
}

# ACL включён: все операции с API требуют токена.
# Bootstrap-токен генерируется при первом запуске setup.sh и сохраняется
# в GitHub Secrets как NOMAD_TOKEN.
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
