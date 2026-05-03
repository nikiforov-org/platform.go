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

variable "PLATFORM_NATS_USER" {
  default = ""
}

variable "PLATFORM_NATS_PASSWORD" {
  default = ""
}

variable "PLATFORM_ALLOWED_HOSTS" {
  description = "Разрешённые HTTP Origin (через запятую), например: example.com,api.example.com"
  default     = ""
}

variable "PLATFORM_GATEWAY_AUTH_RATE_PREFIX" {
  default = ""
}

variable "PLATFORM_GATEWAY_TRUSTED_PROXY" {
  description = "IP доверенного прокси/LB (Cloudflare, балансировщик). Пустой — X-Real-IP игнорируется."
  default     = ""
}

variable "PLATFORM_LOG_LEVEL" {
  default = "info"
}

job "gateway" {
  datacenters = ["dc1"]
  # type = "system" — один alloc на каждую client-ноду. Static port :80 +
  # DNS RR через PLATFORM_DOMAIN требуют gateway на всех нодах: при count=1
  # N-1 нод отвечали бы connection refused. Rolling update идёт по одной ноде
  # за раз (max_parallel=1) — окно недоступности только на обновляемой,
  # остальные продолжают принимать трафик.
  type = "system"

  update {
    max_parallel      = 1
    min_healthy_time  = "10s"
    healthy_deadline  = "3m"
    progress_deadline = "10m"
    auto_revert       = true
  }

  group "gateway" {
    network {
      port "http" {
        static = 80
      }
    }

    restart {
      attempts = 10
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    task "gateway" {
      driver       = "raw_exec"
      user         = "root" # Порт 80 требует привилегий для bind
      kill_timeout = "30s"

      logs {
        max_files     = 5
        max_file_size = 10
      }

      artifact {
        source      = "https://github.com/${var.GITHUB_REPO}/releases/download/${var.VERSION}/gateway_linux_${var.ARCH}.tar.gz"
        destination = "local/"

        options {
          checksum = var.CHECKSUM
        }
      }

      config {
        command = "local/gateway"
      }

      env {
        PLATFORM_NATS_HOST                = "127.0.0.1"
        PLATFORM_NATS_PORT                = "4222"
        PLATFORM_NATS_USER                = var.PLATFORM_NATS_USER
        PLATFORM_NATS_PASSWORD            = var.PLATFORM_NATS_PASSWORD
        PLATFORM_HTTP_ADDR                = ":80"
        PLATFORM_ALLOWED_HOSTS            = var.PLATFORM_ALLOWED_HOSTS
        PLATFORM_GATEWAY_AUTH_RATE_PREFIX = var.PLATFORM_GATEWAY_AUTH_RATE_PREFIX
        PLATFORM_GATEWAY_TRUSTED_PROXY    = var.PLATFORM_GATEWAY_TRUSTED_PROXY
        PLATFORM_LOG_LEVEL                = var.PLATFORM_LOG_LEVEL
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
