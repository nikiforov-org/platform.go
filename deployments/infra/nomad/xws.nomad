# deployments/infra/nomad/xws.nomad
#
# Nomad-джоб xws — пример менеджера WebSocket-сессий через NATS Pub/Sub.
# Переменные в ВЕРХНЕМ РЕГИСТРЕ — имена совпадают с GitHub Secrets.

variable "GITHUB_REPO" {
  default = ""
}

variable "VERSION" {
  default = ""
}

variable "ARCH" {
  default = "amd64"
}

variable "CHECKSUM" {
  description = "SHA256 архива (sha256:<hex>)."
  default     = ""
}

variable "PLATFORM_NATS_USER" {
  default = ""
}

variable "PLATFORM_NATS_PASSWORD" {
  default = ""
}

variable "X_WS_INACTIVITY_TIMEOUT" {
  default = "3m"
}

variable "PLATFORM_LOG_LEVEL" {
  default = "info"
}

variable "NODES" {
  description = "Число ready-нод кластера; определяется на prod-сервере через Nomad API. count копий = min(NODES, 3), distinct_hosts обеспечивает размещение на разных нодах."
  default     = 1
}

job "xws" {
  datacenters = ["dc1"]
  type        = "service"

  update {
    max_parallel      = 1
    min_healthy_time  = "10s"
    healthy_deadline  = "3m"
    progress_deadline = "10m"
    auto_revert       = true
  }

  group "xws" {
    # min(NODES, 3): при 1-2 нодах — по одной копии на ноду; при 3+ — 3 копии.
    # distinct_hosts гарантирует размещение копий на разных нодах.
    count = min(var.NODES, 3)

    constraint {
      distinct_hosts = true
    }

    # Dynamic port для HTTP /healthz — probe через NATS-mux (P-M9).
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
      user         = "nomad" # I-H6: task от непривилегированного user'а, не от root
      kill_timeout = "30s"

      logs {
        max_files     = 5
        max_file_size = 10
      }

      artifact {
        source      = "https://github.com/${var.GITHUB_REPO}/releases/download/${var.VERSION}/xws_linux_${var.ARCH}.tar.gz"
        destination = "local/"

        options {
          checksum = var.CHECKSUM
        }
      }

      config {
        command = "local/xws"
      }

      env {
        PLATFORM_NATS_HOST          = "127.0.0.1"
        PLATFORM_NATS_PORT          = "4222"
        PLATFORM_NATS_USER          = var.PLATFORM_NATS_USER
        PLATFORM_NATS_PASSWORD      = var.PLATFORM_NATS_PASSWORD
        X_WS_INACTIVITY_TIMEOUT = var.X_WS_INACTIVITY_TIMEOUT
        X_HEALTH_ADDR        = "${NOMAD_IP_health}:${NOMAD_PORT_health}"
        PLATFORM_LOG_LEVEL          = var.PLATFORM_LOG_LEVEL
      }

      resources {
        cpu    = 100
        memory = 32
      }
    }
  }
}
