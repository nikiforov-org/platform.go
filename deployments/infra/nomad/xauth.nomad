# deployments/infra/nomad/xauth.nomad
#
# Nomad-джоб xauth — пример JWT-аутентификации с HttpOnly-куками и NATS KV.
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

variable "AUTH_USERNAME" {
  default = ""
}

variable "AUTH_PASSWORD" {
  default = ""
}

variable "AUTH_ACCESS_SECRET" {
  description = "HMAC-ключ для подписи access JWT."
  default     = ""
}

variable "AUTH_REFRESH_SECRET" {
  default = ""
}

variable "AUTH_ACCESS_TTL" {
  default = "15m"
}

variable "AUTH_REFRESH_TTL" {
  default = "168h"
}

variable "COOKIE_DOMAIN" {
  default = ""
}

variable "COOKIE_SECURE" {
  default = "true"
}

variable "LOG_LEVEL" {
  default = "info"
}

job "xauth" {
  datacenters = ["dc1"]
  type        = "service"

  update {
    max_parallel      = 1
    min_healthy_time  = "10s"
    healthy_deadline  = "3m"
    progress_deadline = "10m"
    auto_revert       = true
  }

  group "xauth" {
    count = 1

    # Dynamic port для HTTP /healthz. Nomad выделяет свободный порт на 127.0.0.1,
    # сервис получает его через ${NOMAD_PORT_health}. Probe идёт через тот же
    # NATS-mux, что и бизнес-handler'ы, — ловит deadlock-в-handler (см. P-M9).
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
      name     = "xauth"
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

    task "xauth" {
      driver       = "raw_exec"
      user         = "nomad" # I-H6: task от непривилегированного user'а, не от root
      kill_timeout = "30s"

      logs {
        max_files     = 5
        max_file_size = 10
      }

      artifact {
        source      = "https://github.com/${var.GITHUB_REPO}/releases/download/${var.VERSION}/xauth_linux_${var.ARCH}.tar.gz"
        destination = "local/"

        options {
          checksum = var.CHECKSUM
        }
      }

      config {
        command = "local/xauth"
      }

      env {
        NATS_HOST           = "127.0.0.1"
        NATS_PORT           = "4222"
        NATS_USER           = var.NATS_USER
        NATS_PASSWORD       = var.NATS_PASSWORD
        AUTH_USERNAME       = var.AUTH_USERNAME
        AUTH_PASSWORD       = var.AUTH_PASSWORD
        AUTH_ACCESS_SECRET  = var.AUTH_ACCESS_SECRET
        AUTH_REFRESH_SECRET = var.AUTH_REFRESH_SECRET
        AUTH_ACCESS_TTL     = var.AUTH_ACCESS_TTL
        AUTH_REFRESH_TTL    = var.AUTH_REFRESH_TTL
        COOKIE_DOMAIN       = var.COOKIE_DOMAIN
        COOKIE_SECURE       = var.COOKIE_SECURE
        HEALTH_ADDR         = "127.0.0.1:${NOMAD_PORT_health}"
        LOG_LEVEL           = var.LOG_LEVEL
      }

      resources {
        cpu    = 100
        memory = 32
      }
    }
  }
}
