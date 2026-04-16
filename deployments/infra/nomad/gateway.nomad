# deployments/infra/nomad/gateway.nomad
#
# Nomad-джоб Gateway — единственная точка входа HTTP→NATS.
# Переменные в ВЕРХНЕМ РЕГИСТРЕ — имена совпадают с GitHub Secrets.
#
# Деплой вручную:
#   nomad job run \
#     -var VERSION=v1.2.3 \
#     -var GITHUB_REPO=owner/repo \
#     deployments/infra/nomad/gateway.nomad

variable "GITHUB_REPO" {
  description = "GitHub репозиторий в формате owner/repo"
  default     = ""
}

variable "VERSION" {
  description = "Версия релиза, например: v1.2.3 или build-42"
  default     = ""
}

variable "ARCH" {
  description = "Архитектура: amd64 или arm64"
  default     = "amd64"
}

variable "CHECKSUM" {
  description = "SHA256 архива (sha256:<hex>). Передаётся CI — защищает от подмены бинарника."
  default     = ""
}

variable "NATS_USER" {
  default = ""
}

variable "NATS_PASSWORD" {
  default = ""
}

variable "ALLOWED_HOSTS" {
  description = "Разрешённые HTTP Origin (через запятую), например: example.com,api.example.com"
  default     = ""
}

variable "GATEWAY_AUTH_RATE_PREFIX" {
  default = ""
}

variable "GATEWAY_TRUSTED_PROXY" {
  description = "IP доверенного прокси/LB (Cloudflare, балансировщик). Пустой — X-Real-IP игнорируется."
  default     = ""
}

variable "LOG_LEVEL" {
  default = "info"
}

job "gateway" {
  datacenters = ["dc1"]
  type        = "service"

  update {
    max_parallel      = 1
    min_healthy_time  = "10s"
    healthy_deadline  = "3m"
    progress_deadline = "10m"
    auto_revert       = true
  }

  group "gateway" {
    count = 1

    network {
      port "http" {
        static = 8080
      }
    }

    logs {
      max_files     = 5
      max_file_size = 10
    }

    restart {
      attempts = 10
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    task "gateway" {
      driver       = "raw_exec"
      kill_timeout = "30s"

      artifact {
        source      = "https://github.com/${var.GITHUB_REPO}/releases/download/${var.VERSION}/gateway_linux_${var.ARCH}.tar.gz"
        destination = "local/"
        checksum    = var.CHECKSUM
      }

      config {
        command = "local/gateway"
      }

      env {
        NATS_HOST                = "127.0.0.1"
        NATS_PORT                = "4222"
        NATS_USER                = var.NATS_USER
        NATS_PASSWORD            = var.NATS_PASSWORD
        HTTP_ADDR                = ":8080"
        ALLOWED_HOSTS            = var.ALLOWED_HOSTS
        GATEWAY_AUTH_RATE_PREFIX = var.GATEWAY_AUTH_RATE_PREFIX
        GATEWAY_TRUSTED_PROXY    = var.GATEWAY_TRUSTED_PROXY
        LOG_LEVEL                = var.LOG_LEVEL
      }

      service {
        name     = "gateway"
        port     = "http"
        provider = "nomad"

        check {
          name     = "http-health"
          type     = "http"
          path     = "/health"
          interval = "10s"
          timeout  = "3s"
        }
      }

      resources {
        cpu    = 200
        memory = 64
      }
    }
  }
}
