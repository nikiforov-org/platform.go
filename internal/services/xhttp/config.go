// internal/services/xhttp/config.go
package xhttp

import (
	"os"
	"time"

	"platform/internal/platform/nc"
	"platform/utils"
)

// Config — конфигурация сервиса xhttp.
//
// Переменные окружения:
//
//	NATS_HOST         — хост NATS-сервера      ("127.0.0.1")
//	NATS_PORT         — порт NATS-сервера       (4222)
//	NATS_USER         — логин авторизации       ("")
//	NATS_PASSWORD     — пароль авторизации      ("")
//	NATS_KV_REPLICAS  — число реплик KV-бакета  (3; для dev: 1)
//	DATABASE_URL      — DSN PostgreSQL           (обязательно)
//	CACHE_TTL         — TTL кэша                ("30s")
type Config struct {
	NATS        nc.Config
	DatabaseURL string
	CacheTTL    time.Duration
}

// LoadConfig читает конфигурацию из переменных окружения.
func LoadConfig() Config {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log := utils.Logger()
		log.Fatal().Str("key", "DATABASE_URL").Msg("обязательная переменная окружения не задана")
	}

	natsCfg := nc.DefaultConfig()
	natsCfg.Server.Host = utils.GetEnv("NATS_HOST", natsCfg.Server.Host)
	natsCfg.Server.ClientPort = utils.GetEnv("NATS_PORT", natsCfg.Server.ClientPort)
	natsCfg.Auth.User = utils.GetEnv("NATS_USER", "")
	natsCfg.Auth.Password = utils.GetEnv("NATS_PASSWORD", "")
	// Собственный KV-бакет сервиса, изолированный от platform_state.
	natsCfg.KV.BucketName = "xhttp_cache"
	natsCfg.KV.Replicas = utils.GetEnv("NATS_KV_REPLICAS", natsCfg.KV.Replicas)
	natsCfg.KV.History = 1 // Кэш не нуждается в истории ревизий.

	return Config{
		NATS:        natsCfg,
		DatabaseURL: dbURL,
		CacheTTL:    utils.GetEnv("CACHE_TTL", 30*time.Second),
	}
}
