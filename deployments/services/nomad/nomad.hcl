# deployments/services/nomad/nomad.hcl
#
# Конфигурация Nomad-агента в гибридном режиме (server + client).
# Один файл для всех нод.
#
# Переменные окружения (задаются в systemd-юните):
#   NOMAD_BOOTSTRAP_EXPECT — число server-нод для формирования кластера
#
# Пример systemd-юнита (/etc/systemd/system/nomad.service):
#   [Service]
#   Environment=NOMAD_BOOTSTRAP_EXPECT=3
#   ExecStart=/usr/local/bin/nomad agent -config=/etc/nomad/nomad.hcl

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
  bootstrap_expect = "${NOMAD_BOOTSTRAP_EXPECT}"

  job_gc_threshold        = "4h"
  eval_gc_threshold       = "4h"
  deployment_gc_threshold = "4h"
  node_gc_threshold       = "24h"
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
