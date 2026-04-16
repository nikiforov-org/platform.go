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

    task "xauth" {
      driver       = "raw_exec"
      kill_timeout = "30s"

      artifact {
        source      = "https://github.com/${var.GITHUB_REPO}/releases/download/${var.VERSION}/xauth_linux_${var.ARCH}.tar.gz"
        destination = "local/"
        checksum    = var.CHECKSUM
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
        LOG_LEVEL           = var.LOG_LEVEL
      }

      resources {
        cpu    = 100
        memory = 32
      }
    }
  }
}
