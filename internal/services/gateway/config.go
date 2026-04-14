// internal/services/gateway/config.go
package gateway

import (
	"fmt"
	"os"
	"time"

	"platform/internal/platform/nc"
	"platform/utils"
)

// Config — полная конфигурация шлюза, собранная из переменных окружения.
// Является единственным источником конфигурации для cmd/gateway/main.go:
// никаких os.Getenv за пределами LoadConfig.
type Config struct {
	// HTTP — параметры входящего HTTP-сервера.
	HTTP HTTPConfig

	// NATS — параметры подключения к шине.
	NATS nc.Config

	// AllowedHosts — множество разрешённых Origin-хостов.
	// Пустое множество отключает проверку (dev-режим).
	AllowedHosts utils.AllowedHostSet

	// RateLimit — ограничения входящего трафика.
	RateLimit RateLimitConfig
}

// RateLimitConfig — параметры rate limiting для входящего трафика.
type RateLimitConfig struct {
	// Rate — максимальная скорость запросов в секунду с одного IP (общий лимит).
	Rate float64

	// Burst — пиковый размер очереди (общий лимит). Клиент может послать Burst
	// запросов мгновенно, после чего ограничен скоростью Rate req/s.
	Burst int

	// AuthPathPrefix — URL-префикс, к которому применяется дополнительный жёсткий
	// per-IP лимит (AuthRate/AuthBurst). Используется для защиты от брутфорса
	// на маршрутах аутентификации. Если пустой — второй лимит не применяется.
	AuthPathPrefix string

	// AuthRate — максимальная скорость запросов в секунду с одного IP
	// для маршрутов под AuthPathPrefix.
	AuthRate float64

	// AuthBurst — пиковый размер очереди для маршрутов под AuthPathPrefix.
	AuthBurst int

	// MaxWSConns — максимальное число одновременных WebSocket-соединений.
	// При превышении Gateway возвращает 503.
	MaxWSConns int64
}

// HTTPConfig — параметры HTTP-сервера шлюза.
type HTTPConfig struct {
	// Addr — адрес и порт для входящих запросов (HTTP_ADDR).
	Addr string

	// ReadHeaderTimeout — защита от Slowloris-атак (HTTP_READ_HEADER_TIMEOUT).
	ReadHeaderTimeout time.Duration

	// ReadTimeout — таймаут на чтение всего запроса (HTTP_READ_TIMEOUT).
	ReadTimeout time.Duration

	// WriteTimeout — таймаут на запись ответа (HTTP_WRITE_TIMEOUT).
	// Для WebSocket-соединений апгрейдер сбрасывает его до нуля автоматически.
	WriteTimeout time.Duration

	// IdleTimeout — таймаут keep-alive соединений (HTTP_IDLE_TIMEOUT).
	IdleTimeout time.Duration
}

// LoadConfig читает все параметры конфигурации из переменных окружения.
//
// Переменные окружения и их значения по умолчанию:
//
//	HTTP_ADDR                  — адрес сервера                           (":8080")
//	HTTP_READ_HEADER_TIMEOUT   — таймаут чтения заголовков (формат: 5s)  ("5s")
//	HTTP_READ_TIMEOUT          — таймаут чтения запроса    (формат: 15s) ("15s")
//	HTTP_WRITE_TIMEOUT         — таймаут записи ответа     (формат: 15s) ("15s")
//	HTTP_IDLE_TIMEOUT          — таймаут keep-alive        (формат: 60s) ("60s")
//
//	NATS_HOST                  — хост NATS-сервера                       ("127.0.0.1")
//	NATS_PORT                  — клиентский порт NATS                    (4222)
//	NATS_USER                  — логин авторизации                       ("")
//	NATS_PASSWORD              — пароль авторизации                       ("")
//	NATS_RECONNECT_ATTEMPTS    — число попыток реконнекта (-1 = ∞)       (-1)
//	NATS_RECONNECT_WAIT        — пауза между попытками    (формат: 2s)   ("2s")
//
//	ALLOWED_HOSTS              — разрешённые Origin-хосты через ","      ("")
//
// Таймауты передаются в формате time.Duration: "5s", "1m30s" и т.д.
//
//	GATEWAY_RATE_LIMIT         — req/s с одного IP (общий)               (100)
//	GATEWAY_RATE_BURST         — burst (общий)                            (200)
//	GATEWAY_AUTH_RATE_PREFIX   — URL-префикс для жёсткого лимита         ("")
//	GATEWAY_AUTH_RATE_LIMIT    — req/s с одного IP для auth-префикса     (5)
//	GATEWAY_AUTH_RATE_BURST    — burst для auth-префикса                  (10)
//	GATEWAY_MAX_WS_CONNS       — макс. одновременных WS-соединений        (1000)
func LoadConfig() (Config, error) {
	// ALLOWED_HOSTS читается через os.Getenv, а не utils.GetEnv: значение содержит
	// запятые — fmt.Sscan остановился бы на первом разделителе.
	// Парсинг делегируется utils.ParseAllowedHosts.
	allowedHosts, err := utils.ParseAllowedHosts(os.Getenv("ALLOWED_HOSTS"))
	if err != nil {
		return Config{}, fmt.Errorf("ALLOWED_HOSTS: %w", err)
	}

	natsCfg := nc.DefaultConfig()
	natsCfg.Server.Host = utils.GetEnv("NATS_HOST", natsCfg.Server.Host)
	natsCfg.Server.ClientPort = utils.GetEnv("NATS_PORT", natsCfg.Server.ClientPort)
	natsCfg.Auth.User = utils.GetEnv("NATS_USER", "")
	natsCfg.Auth.Password = utils.GetEnv("NATS_PASSWORD", "")
	natsCfg.Reconnect.MaxAttempts = utils.GetEnv("NATS_RECONNECT_ATTEMPTS", natsCfg.Reconnect.MaxAttempts)
	natsCfg.Reconnect.WaitDuration = utils.GetEnv("NATS_RECONNECT_WAIT", 2*time.Second)
	natsCfg.KV.BucketName = "" // Gateway не использует KV — инициализация бакета не нужна.

	return Config{
		HTTP: HTTPConfig{
			Addr:              utils.GetEnv("HTTP_ADDR", ":8080"),
			ReadHeaderTimeout: utils.GetEnv("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:       utils.GetEnv("HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:      utils.GetEnv("HTTP_WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:       utils.GetEnv("HTTP_IDLE_TIMEOUT", 60*time.Second),
		},
		NATS:         natsCfg,
		AllowedHosts: allowedHosts,
		RateLimit: RateLimitConfig{
			Rate:           utils.GetEnv("GATEWAY_RATE_LIMIT", 100.0),
			Burst:          utils.GetEnv("GATEWAY_RATE_BURST", 200),
			AuthPathPrefix: utils.GetEnv("GATEWAY_AUTH_RATE_PREFIX", ""),
			AuthRate:       utils.GetEnv("GATEWAY_AUTH_RATE_LIMIT", 5.0),
			AuthBurst:      utils.GetEnv("GATEWAY_AUTH_RATE_BURST", 10),
			MaxWSConns:     utils.GetEnv("GATEWAY_MAX_WS_CONNS", int64(1000)),
		},
	}, nil
}
