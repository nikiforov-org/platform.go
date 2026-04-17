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

variable "NATS_USER" {
  default = ""
}

variable "NATS_PASSWORD" {
  default = ""
}

variable "INACTIVITY_TIMEOUT" {
  default = "3m"
}

variable "LOG_LEVEL" {
  default = "info"
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
    count = 1

    restart {
      attempts = 10
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
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
        NATS_HOST          = "127.0.0.1"
        NATS_PORT          = "4222"
        NATS_USER          = var.NATS_USER
        NATS_PASSWORD      = var.NATS_PASSWORD
        INACTIVITY_TIMEOUT = var.INACTIVITY_TIMEOUT
        LOG_LEVEL          = var.LOG_LEVEL
      }

      resources {
        cpu    = 100
        memory = 32
      }
    }
  }
}
