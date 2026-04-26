// internal/services/gateway/config.go
package gateway

import (
	"fmt"
	"os"
	"time"

	"platform/internal/platform/nc"
	"platform/utils"

	"github.com/rs/zerolog"
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

	// TrustedProxy — IP доверенного обратного прокси (Cloudflare, LB).
	// Если задан, заголовок X-Real-IP принимается только от этого IP.
	// Если пустой — X-Real-IP из входящего запроса игнорируется, используется
	// r.RemoteAddr. Задаётся через GATEWAY_TRUSTED_PROXY.
	TrustedProxy string

	// MaxIPs — максимальное число IP-адресов в каждой таблице rate limiter.
	// При достижении лимита самая старая запись вытесняется перед добавлением новой.
	// Ограничивает потребление памяти при DDoS с разных IP.
	// Задаётся через GATEWAY_RATE_LIMIT_MAX_IPS. По умолчанию 100 000.
	MaxIPs int
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

	// NATSRequestTimeout — максимальное время ожидания ответа от бекенда через NATS
	// Request-Reply (GATEWAY_NATS_REQUEST_TIMEOUT). Гарантирует, что gateway-горутина
	// не зависает дольше заданного, даже если клиент продолжает ждать.
	NATSRequestTimeout time.Duration

	// NATSRetryDelay — пауза перед повтором запроса при nats.ErrNoResponders.
	// Повторы идут, пока не истёк NATSRequestTimeout; это закрывает короткое окно
	// между падением ноды и появлением живой копии сервиса-получателя на другой
	// ноде. Задаётся через GATEWAY_NATS_RETRY_DELAY.
	NATSRetryDelay time.Duration

	// WSConnectTimeout — таймаут ожидания ack от целевого сервиса при открытии
	// WebSocket-сессии (GATEWAY_WS_CONNECT_TIMEOUT). Если сервис не подписан или
	// не успел ответить — Gateway закрывает WS-соединение с кодом 1011, чтобы
	// клиент не висел с «зомби»-WS до wsReadDeadline.
	WSConnectTimeout time.Duration
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
//	GATEWAY_NATS_REQUEST_TIMEOUT — таймаут ожидания ответа из NATS (5s)  ("5s")
//	GATEWAY_NATS_RETRY_DELAY   — пауза перед повтором при ErrNoResponders ("100ms")
//	GATEWAY_WS_CONNECT_TIMEOUT — таймаут ack от сервиса при WS connect    ("2s")
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
//	GATEWAY_MAX_WS_CONNS         — макс. одновременных WS-соединений        (1000)
//	GATEWAY_TRUSTED_PROXY        — IP доверенного прокси (Cloudflare, LB)   ("")
//	GATEWAY_RATE_LIMIT_MAX_IPS   — макс. IP в таблице rate limiter           (100000)
func LoadConfig(log zerolog.Logger) (Config, error) {
	// ALLOWED_HOSTS читается через os.Getenv, а не utils.GetEnv: значение содержит
	// запятые — fmt.Sscan остановился бы на первом разделителе.
	// Парсинг делегируется utils.ParseAllowedHosts.
	allowedHosts, err := utils.ParseAllowedHosts(log, os.Getenv("ALLOWED_HOSTS"))
	if err != nil {
		return Config{}, fmt.Errorf("ALLOWED_HOSTS: %w", err)
	}

	natsCfg := nc.DefaultConfig()
	natsCfg.Server.Host = utils.GetEnv(log, "NATS_HOST", natsCfg.Server.Host)
	natsCfg.Server.ClientPort = utils.GetEnv(log, "NATS_PORT", natsCfg.Server.ClientPort)
	natsCfg.Auth.User = utils.GetEnv(log, "NATS_USER", "")
	natsCfg.Auth.Password = utils.GetEnv(log, "NATS_PASSWORD", "")
	natsCfg.Reconnect.MaxAttempts = utils.GetEnv(log, "NATS_RECONNECT_ATTEMPTS", natsCfg.Reconnect.MaxAttempts)
	natsCfg.Reconnect.WaitDuration = utils.GetEnv(log, "NATS_RECONNECT_WAIT", 2*time.Second)
	natsCfg.KV.BucketName = "" // Gateway не использует KV — инициализация бакета не нужна.

	cfg := Config{
		HTTP: HTTPConfig{
			Addr:              utils.GetEnv(log, "HTTP_ADDR", ":8080"),
			ReadHeaderTimeout: utils.GetEnv(log, "HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:       utils.GetEnv(log, "HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:      utils.GetEnv(log, "HTTP_WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:        utils.GetEnv(log, "HTTP_IDLE_TIMEOUT", 60*time.Second),
			NATSRequestTimeout: utils.GetEnv(log, "GATEWAY_NATS_REQUEST_TIMEOUT", 5*time.Second),
			NATSRetryDelay:     utils.GetEnv(log, "GATEWAY_NATS_RETRY_DELAY", 100*time.Millisecond),
			WSConnectTimeout:   utils.GetEnv(log, "GATEWAY_WS_CONNECT_TIMEOUT", 2*time.Second),
		},
		NATS:         natsCfg,
		AllowedHosts: allowedHosts,
		RateLimit: RateLimitConfig{
			Rate:           utils.GetEnv(log, "GATEWAY_RATE_LIMIT", 100.0),
			Burst:          utils.GetEnv(log, "GATEWAY_RATE_BURST", 200),
			AuthPathPrefix: utils.GetEnv(log, "GATEWAY_AUTH_RATE_PREFIX", ""),
			AuthRate:       utils.GetEnv(log, "GATEWAY_AUTH_RATE_LIMIT", 5.0),
			AuthBurst:      utils.GetEnv(log, "GATEWAY_AUTH_RATE_BURST", 10),
			MaxWSConns:     utils.GetEnv(log, "GATEWAY_MAX_WS_CONNS", int64(1000)),
			TrustedProxy:   utils.GetEnv(log, "GATEWAY_TRUSTED_PROXY", ""),
			MaxIPs:         utils.GetEnv(log, "GATEWAY_RATE_LIMIT_MAX_IPS", 100_000),
		},
	}

	if err := validateRateLimit(cfg.RateLimit); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// validateRateLimit отбивает misconfig оператора на старте: 0 или negative
// значения проходили GetEnv молча и приводили к silent degradation
// (rate.Limit(0) отклоняет всё, MaxIPs<=0 ломает eviction-условие, MaxWSConns<=0
// отвергает все WS). Лучше fail-fast в LoadConfig — оператор видит ошибку сразу.
func validateRateLimit(rl RateLimitConfig) error {
	if rl.Rate <= 0 {
		return fmt.Errorf("GATEWAY_RATE_LIMIT must be > 0, got %v", rl.Rate)
	}
	if rl.Burst <= 0 {
		return fmt.Errorf("GATEWAY_RATE_BURST must be > 0, got %d", rl.Burst)
	}
	if rl.MaxIPs <= 0 {
		return fmt.Errorf("GATEWAY_RATE_LIMIT_MAX_IPS must be > 0, got %d", rl.MaxIPs)
	}
	if rl.MaxWSConns <= 0 {
		return fmt.Errorf("GATEWAY_MAX_WS_CONNS must be > 0, got %d", rl.MaxWSConns)
	}
	if rl.AuthPathPrefix != "" {
		if rl.AuthRate <= 0 {
			return fmt.Errorf("GATEWAY_AUTH_RATE_LIMIT must be > 0 when GATEWAY_AUTH_RATE_PREFIX is set, got %v", rl.AuthRate)
		}
		if rl.AuthBurst <= 0 {
			return fmt.Errorf("GATEWAY_AUTH_RATE_BURST must be > 0 when GATEWAY_AUTH_RATE_PREFIX is set, got %d", rl.AuthBurst)
		}
	}
	return nil
}
