// internal/services/xws/config.go
package xws

import (
	"time"

	"platform/internal/platform/nc"
	"platform/utils"

	"github.com/rs/zerolog"
)

// Config — конфигурация сервиса xws.
//
// Переменные окружения:
//
//	PLATFORM_NATS_HOST            — хост NATS-сервера      ("127.0.0.1")
//	PLATFORM_NATS_PORT            — порт NATS-сервера       (4222)
//	PLATFORM_NATS_USER            — логин авторизации       ("")
//	PLATFORM_NATS_PASSWORD        — пароль авторизации      ("")
//	X_WS_INACTIVITY_TIMEOUT   — таймаут бездействия    ("3m")
type Config struct {
	NATS              nc.Config
	InactivityTimeout time.Duration
}

// LoadConfig читает конфигурацию из переменных окружения.
func LoadConfig(log zerolog.Logger) Config {
	natsCfg := nc.DefaultConfig()
	natsCfg.Server.Host = utils.GetEnv(log, "PLATFORM_NATS_HOST", natsCfg.Server.Host)
	natsCfg.Server.ClientPort = utils.GetEnv(log, "PLATFORM_NATS_PORT", natsCfg.Server.ClientPort)
	natsCfg.Auth.User = utils.GetEnv(log, "PLATFORM_NATS_USER", "")
	natsCfg.Auth.Password = utils.GetEnv(log, "PLATFORM_NATS_PASSWORD", "")
	natsCfg.KV.BucketName = "" // xws не использует KV-хранилище.

	return Config{
		NATS:              natsCfg,
		InactivityTimeout: utils.GetEnv(log, "X_WS_INACTIVITY_TIMEOUT", 3*time.Minute),
	}
}
