# deployments/infra/nomad/nomad.hcl
#
# Конфигурация Nomad-агента в гибридном режиме (server + client).
# Один файл для всех нод.
#
# Переменные окружения (задаются в /etc/nomad/env, раскрываются Nomad при старте):
#   PLATFORM_DOMAIN — домен A-записей кластера (все ноды)
#
# bootstrap_expect = 1: нода сразу готова к работе.
# server_join retry_join: при наличии других нод в DNS автоматически входит
# в существующий кластер; при отсутствии — работает как single-node кластер.

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
