# Makefile — команды разработки платформы.
#
# Предварительно скопируйте deployments/dev/.env.example → deployments/dev/.env
# и заполните реальными значениями.

-include deployments/dev/.env
export

.PHONY: build infra infra-down run-gateway run-xauth run-xhttp run-xws

# Сборка всех бинарников.
build:
	go build ./cmd/...

# Запустить инфраструктуру (NATS + PostgreSQL) в фоне.
infra:
	docker compose -f deployments/dev/docker-compose.yml up -d

# Остановить инфраструктуру.
infra-down:
	docker compose -f deployments/dev/docker-compose.yml down

# --- Запуск отдельных сервисов ---

run-gateway:
	go run ./cmd/gateway

run-xauth:
	go run ./cmd/xauth

run-xhttp:
	go run ./cmd/xhttp

run-xws:
	go run ./cmd/xws
