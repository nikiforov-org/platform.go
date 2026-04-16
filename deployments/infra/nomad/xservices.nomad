# deployments/infra/nomad/xservices.nomad
#
# Nomad-джоб демо-сервисов (xauth, xhttp, xws).
# Бинарники скачиваются из GitHub Releases через блок artifact.
#
# Деплой:
#   nomad job run \
#     -var-file=deployments/envs/prod/prod.vars \
#     deployments/infra/nomad/xservices.nomad

variable "github_repo" {
  description = "GitHub репозиторий в формате owner/repo, например: myorg/platform.go"
  default     = ""
}

variable "version" {
  description = "Версия релиза, например: v1.2.3"
  default     = ""
}

variable "arch" {
  description = "Архитектура: amd64 или arm64"
  default     = "amd64"
}

variable "nats_user" {
  default = ""
}

variable "nats_password" {
  default = ""
}

variable "auth_username" {
  default = ""
}

variable "auth_password" {
  default = ""
}

variable "auth_access_secret" {
  description = "HMAC-ключ для подписи access JWT. Должен совпадать с access_secret."
  default     = ""
}

variable "auth_refresh_secret" {
  default = ""
}

variable "auth_access_ttl" {
  default = "15m"
}

variable "auth_refresh_ttl" {
  default = "168h"
}

variable "cookie_domain" {
  default = ""
}

variable "cookie_secure" {
  default = "true"
}

variable "database_url" {
  description = "PostgreSQL DSN для xhttp."
  default     = ""
}

variable "access_secret" {
  description = "HMAC-ключ для валидации access JWT в xhttp/xws. Должен совпадать с auth_access_secret."
  default     = ""
}

variable "cache_ttl" {
  default = "30s"
}

variable "inactivity_timeout" {
  default = "3m"
}

variable "checksum_xauth" {
  description = "SHA256 архива xauth (sha256:<hex>). Передаётся CI — защищает от подмены бинарника."
  default     = ""
}

variable "checksum_xhttp" {
  description = "SHA256 архива xhttp (sha256:<hex>). Передаётся CI — защищает от подмены бинарника."
  default     = ""
}

variable "checksum_xws" {
  description = "SHA256 архива xws (sha256:<hex>). Передаётся CI — защищает от подмены бинарника."
  default     = ""
}

variable "log_level" {
  default = "info"
}

job "xservices" {
  datacenters = ["dc1"]
  type        = "service"

  update {
    max_parallel      = 1
    min_healthy_time  = "10s"
    healthy_deadline  = "3m"
    progress_deadline = "10m"
    auto_revert       = true
  }

  # ==========================================================================
  # xauth — пример JWT-аутентификации с HttpOnly-куками и NATS KV.
  # ==========================================================================
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
        source      = "https://github.com/${var.github_repo}/releases/download/${var.version}/xauth_linux_${var.arch}.tar.gz"
        destination = "local/"
        checksum    = var.checksum_xauth
      }

      config {
        command = "local/xauth"
      }

      env {
        NATS_HOST           = "127.0.0.1"
        NATS_PORT           = "4222"
        NATS_USER           = var.nats_user
        NATS_PASSWORD       = var.nats_password
        AUTH_USERNAME       = var.auth_username
        AUTH_PASSWORD       = var.auth_password
        AUTH_ACCESS_SECRET  = var.auth_access_secret
        AUTH_REFRESH_SECRET = var.auth_refresh_secret
        AUTH_ACCESS_TTL     = var.auth_access_ttl
        AUTH_REFRESH_TTL    = var.auth_refresh_ttl
        COOKIE_DOMAIN       = var.cookie_domain
        COOKIE_SECURE       = var.cookie_secure
        LOG_LEVEL           = var.log_level
      }

      resources {
        cpu    = 100
        memory = 32
      }
    }
  }

  # ==========================================================================
  # xhttp — пример CRUD-сервиса с PostgreSQL + NATS KV кэш.
  # ==========================================================================
  group "xhttp" {
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

    task "xhttp" {
      driver       = "raw_exec"
      kill_timeout = "30s"

      artifact {
        source      = "https://github.com/${var.github_repo}/releases/download/${var.version}/xhttp_linux_${var.arch}.tar.gz"
        destination = "local/"
        checksum    = var.checksum_xhttp
      }

      config {
        command = "local/xhttp"
      }

      env {
        NATS_HOST     = "127.0.0.1"
        NATS_PORT     = "4222"
        NATS_USER     = var.nats_user
        NATS_PASSWORD = var.nats_password
        DATABASE_URL  = var.database_url
        ACCESS_SECRET = var.access_secret
        CACHE_TTL     = var.cache_ttl
        LOG_LEVEL     = var.log_level
      }

      resources {
        cpu    = 100
        memory = 64
      }
    }
  }

  # ==========================================================================
  # xws — пример менеджера WebSocket-сессий через NATS Pub/Sub.
  # ==========================================================================
  group "xws" {
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

    task "xws" {
      driver       = "raw_exec"
      kill_timeout = "30s"

      artifact {
        source      = "https://github.com/${var.github_repo}/releases/download/${var.version}/xws_linux_${var.arch}.tar.gz"
        destination = "local/"
        checksum    = var.checksum_xws
      }

      config {
        command = "local/xws"
      }

      env {
        NATS_HOST          = "127.0.0.1"
        NATS_PORT          = "4222"
        NATS_USER          = var.nats_user
        NATS_PASSWORD      = var.nats_password
        INACTIVITY_TIMEOUT = var.inactivity_timeout
        LOG_LEVEL          = var.log_level
      }

      resources {
        cpu    = 100
        memory = 32
      }
    }
  }
}
