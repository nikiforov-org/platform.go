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
//	NATS_HOST         — хост NATS-сервера      ("127.0.0.1")
//	NATS_PORT         — порт NATS-сервера       (4222)
//	NATS_USER         — логин авторизации       ("")
//	NATS_PASSWORD     — пароль авторизации      ("")
//	DATABASE_URL   — DSN PostgreSQL      (обязательно)
//	ACCESS_SECRET  — HMAC-ключ JWT       (обязательно, должен совпадать с AUTH_ACCESS_SECRET сервиса xauth)
//	CACHE_TTL         — TTL кэша                ("30s")
type Config struct {
	NATS         nc.Config
	DatabaseURL  string
	AccessSecret []byte
	CacheTTL     time.Duration
}

// LoadConfig читает конфигурацию из переменных окружения.
func LoadConfig(log zerolog.Logger) Config {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal().Str("key", "DATABASE_URL").Msg("обязательная переменная окружения не задана")
	}
	// Fail-fast: пустой HMAC-ключ принял бы любой токен, подписанный таким же
	// пустым ключом. Должен совпадать с AUTH_ACCESS_SECRET сервиса xauth.
	accessSecret := os.Getenv("ACCESS_SECRET")
	if accessSecret == "" {
		log.Fatal().Str("key", "ACCESS_SECRET").Msg("обязательная переменная окружения не задана")
	}

	natsCfg := nc.DefaultConfig()
	natsCfg.Server.Host = utils.GetEnv(log, "NATS_HOST", natsCfg.Server.Host)
	natsCfg.Server.ClientPort = utils.GetEnv(log, "NATS_PORT", natsCfg.Server.ClientPort)
	natsCfg.Auth.User = utils.GetEnv(log, "NATS_USER", "")
	natsCfg.Auth.Password = utils.GetEnv(log, "NATS_PASSWORD", "")
	// Собственный KV-бакет сервиса, изолированный от platform_state.
	natsCfg.KV.BucketName = "xhttp_cache"
	// Replicas не задаётся — NewClient определяет число реплик автоматически.
	natsCfg.KV.History = 1 // Кэш не нуждается в истории ревизий.

	return Config{
		NATS:         natsCfg,
		DatabaseURL:  dbURL,
		AccessSecret: []byte(accessSecret),
		CacheTTL:     utils.GetEnv(log, "CACHE_TTL", 30*time.Second),
	}
}
