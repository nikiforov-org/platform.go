// internal/services/xhttp/config.go
package xhttp

import (
	"os"
	"time"

	"platform/internal/platform/nc"
	"platform/utils"

	"github.com/rs/zerolog"
)

// Config — конфигурация сервиса xhttp.
//
// Переменные окружения:
//
//	PLATFORM_NATS_HOST         — хост NATS-сервера      ("127.0.0.1")
//	PLATFORM_NATS_PORT         — порт NATS-сервера       (4222)
//	PLATFORM_NATS_USER         — логин авторизации       ("")
//	PLATFORM_NATS_PASSWORD     — пароль авторизации      ("")
//	X_HTTP_DATABASE_URL   — DSN PostgreSQL      (обязательно)
//	X_AUTH_ACCESS_SECRET  — HMAC-ключ JWT       (обязательно, общий с сервисом xauth)
//	X_HTTP_CACHE_TTL         — TTL кэша                ("30s")
type Config struct {
	NATS         nc.Config
	DatabaseURL  string
	AccessSecret []byte
	CacheTTL     time.Duration
}

// LoadConfig читает конфигурацию из переменных окружения.
func LoadConfig(log zerolog.Logger) Config {
	dbURL := os.Getenv("X_HTTP_DATABASE_URL")
	if dbURL == "" {
		log.Fatal().Str("key", "X_HTTP_DATABASE_URL").Msg("обязательная переменная окружения не задана")
	}
	accessSecret := os.Getenv("X_AUTH_ACCESS_SECRET")
	if accessSecret == "" {
		log.Fatal().Str("key", "X_AUTH_ACCESS_SECRET").Msg("обязательная переменная окружения не задана")
	}

	natsCfg := nc.DefaultConfig()
	natsCfg.Server.Host = utils.GetEnv(log, "PLATFORM_NATS_HOST", natsCfg.Server.Host)
	natsCfg.Server.ClientPort = utils.GetEnv(log, "PLATFORM_NATS_PORT", natsCfg.Server.ClientPort)
	natsCfg.Auth.User = utils.GetEnv(log, "PLATFORM_NATS_USER", "")
	natsCfg.Auth.Password = utils.GetEnv(log, "PLATFORM_NATS_PASSWORD", "")
	// Собственный KV-бакет сервиса, изолированный от platform_state.
	natsCfg.KV.BucketName = "xhttp_cache"
	// Replicas не задаётся — NewClient определяет число реплик автоматически.
	natsCfg.KV.History = 1 // Кэш не нуждается в истории ревизий.

	return Config{
		NATS:         natsCfg,
		DatabaseURL:  dbURL,
		AccessSecret: []byte(accessSecret),
		CacheTTL:     utils.GetEnv(log, "X_HTTP_CACHE_TTL", 30*time.Second),
	}
}
