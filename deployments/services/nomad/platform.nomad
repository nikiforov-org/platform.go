# deployments/services/nomad/platform.nomad
#
# Nomad-джоб платформы: Gateway.
# Драйвер: raw_exec — бинарники запускаются напрямую, без Docker.
#
# Деплой:
#   nomad job run -var-file=deployments/mode/prod/prod.vars deployments/services/nomad/platform.nomad

variable "binary_dir" {
  description = "Директория с бинарниками сервисов."
  default     = "/usr/local/bin"
}

variable "nats_user" {
  default = ""
}

variable "nats_password" {
  default = ""
}

variable "allowed_hosts" {
  description = "Разрешённые HTTP Origin (через запятую), например: example.com,api.example.com"
  default     = ""
}

variable "gateway_auth_rate_prefix" {
  default = "/v1/xauth/"
}

variable "log_level" {
  default = "info"
}

job "platform" {
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
      max_file_size = 10  # MB
    }

    restart {
      attempts = 10
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    task "gateway" {
      driver = "raw_exec"

      config {
        command = "${var.binary_dir}/gateway"
      }

      env {
        NATS_HOST                = "127.0.0.1"
        NATS_PORT                = "4222"
        NATS_USER                = var.nats_user
        NATS_PASSWORD            = var.nats_password
        HTTP_ADDR                = ":8080"
        ALLOWED_HOSTS            = var.allowed_hosts
        GATEWAY_AUTH_RATE_PREFIX = var.gateway_auth_rate_prefix
        LOG_LEVEL                = var.log_level
      }

      # Self-healing: Nomad перезапустит gateway если /health вернёт не 200.
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
        cpu    = 200  # MHz
        memory = 64   # MB
      }
    }
  }
}
