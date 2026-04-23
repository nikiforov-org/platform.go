# deployments/infra/nomad/xhttp.nomad
#
# Nomad-джоб xhttp — пример CRUD-сервиса с PostgreSQL + NATS KV кэш.
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

variable "DATABASE_URL" {
  description = "PostgreSQL DSN."
  default     = ""
}

variable "ACCESS_SECRET" {
  description = "HMAC-ключ для валидации access JWT. Должен совпадать с AUTH_ACCESS_SECRET в xauth."
  default     = ""
}

variable "CACHE_TTL" {
  default = "30s"
}

variable "LOG_LEVEL" {
  default = "info"
}

job "xhttp" {
  datacenters = ["dc1"]
  type        = "service"

  update {
    max_parallel      = 1
    min_healthy_time  = "10s"
    healthy_deadline  = "3m"
    progress_deadline = "10m"
    auto_revert       = true
  }

  group "xhttp" {
    count = 1

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
      name     = "xhttp"
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

    task "xhttp" {
      driver       = "raw_exec"
      user         = "nomad" # I-H6: task от непривилегированного user'а, не от root
      kill_timeout = "30s"

      logs {
        max_files     = 5
        max_file_size = 10
      }

      artifact {
        source      = "https://github.com/${var.GITHUB_REPO}/releases/download/${var.VERSION}/xhttp_linux_${var.ARCH}.tar.gz"
        destination = "local/"

        options {
          checksum = var.CHECKSUM
        }
      }

      config {
        command = "local/xhttp"
      }

      env {
        NATS_HOST     = "127.0.0.1"
        NATS_PORT     = "4222"
        NATS_USER     = var.NATS_USER
        NATS_PASSWORD = var.NATS_PASSWORD
        DATABASE_URL  = var.DATABASE_URL
        ACCESS_SECRET = var.ACCESS_SECRET
        CACHE_TTL     = var.CACHE_TTL
        HEALTH_ADDR   = "${NOMAD_IP_health}:${NOMAD_PORT_health}"
        LOG_LEVEL     = var.LOG_LEVEL
      }

      resources {
        cpu    = 100
        memory = 64
      }
    }
  }
}
