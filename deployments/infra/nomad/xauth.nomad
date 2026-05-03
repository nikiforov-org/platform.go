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

variable "PLATFORM_NATS_USER" {
  default = ""
}

variable "PLATFORM_NATS_PASSWORD" {
  default = ""
}

variable "X_AUTH_USERNAME" {
  default = ""
}

variable "X_AUTH_PASSWORD" {
  default = ""
}

variable "X_AUTH_ACCESS_SECRET" {
  description = "HMAC-ключ для подписи access JWT."
  default     = ""
}

variable "X_AUTH_REFRESH_SECRET" {
  default = ""
}

variable "X_AUTH_ACCESS_TTL" {
  default = "15m"
}

variable "X_AUTH_REFRESH_TTL" {
  default = "168h"
}

variable "X_AUTH_COOKIE_DOMAIN" {
  default = ""
}

variable "X_AUTH_COOKIE_SECURE" {
  default = "true"
}

variable "X_AUTH_COOKIE_SAMESITE" {
  description = "SameSite-политика кук: strict, lax, none"
  default     = "strict"
}

variable "PLATFORM_LOG_LEVEL" {
  default = "info"
}

variable "NODES" {
  description = "Число ready-нод кластера; определяется на prod-сервере через Nomad API. count копий = min(NODES, 3), distinct_hosts обеспечивает размещение на разных нодах."
  default     = 1
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
    # min(NODES, 3): при 1-2 нодах — по одной копии на ноду (max доступной ёмкости);
    # при 3+ нодах — 3 копии (избыточность на падение любой ноды, без лишних ресурсов).
    # distinct_hosts гарантирует, что две копии не окажутся на одной ноде.
    count = min(var.NODES, 3)

    constraint {
      distinct_hosts = true
    }

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
        PLATFORM_NATS_HOST           = "127.0.0.1"
        PLATFORM_NATS_PORT           = "4222"
        PLATFORM_NATS_USER           = var.PLATFORM_NATS_USER
        PLATFORM_NATS_PASSWORD       = var.PLATFORM_NATS_PASSWORD
        X_AUTH_USERNAME       = var.X_AUTH_USERNAME
        X_AUTH_PASSWORD       = var.X_AUTH_PASSWORD
        X_AUTH_ACCESS_SECRET  = var.X_AUTH_ACCESS_SECRET
        X_AUTH_REFRESH_SECRET = var.X_AUTH_REFRESH_SECRET
        X_AUTH_ACCESS_TTL     = var.X_AUTH_ACCESS_TTL
        X_AUTH_REFRESH_TTL    = var.X_AUTH_REFRESH_TTL
        X_AUTH_COOKIE_DOMAIN       = var.X_AUTH_COOKIE_DOMAIN
        X_AUTH_COOKIE_SECURE       = var.X_AUTH_COOKIE_SECURE
        X_AUTH_COOKIE_SAMESITE     = var.X_AUTH_COOKIE_SAMESITE
        X_HEALTH_ADDR         = "${NOMAD_IP_health}:${NOMAD_PORT_health}"
        PLATFORM_LOG_LEVEL           = var.PLATFORM_LOG_LEVEL
      }

      resources {
        cpu    = 100
        memory = 32
      }
    }
  }
}
