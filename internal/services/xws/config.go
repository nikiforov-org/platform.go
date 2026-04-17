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
//	NATS_HOST            — хост NATS-сервера      ("127.0.0.1")
//	NATS_PORT            — порт NATS-сервера       (4222)
//	NATS_USER            — логин авторизации       ("")
//	NATS_PASSWORD        — пароль авторизации      ("")
//	INACTIVITY_TIMEOUT   — таймаут бездействия    ("3m")
type Config struct {
	NATS              nc.Config
	InactivityTimeout time.Duration
}

// LoadConfig читает конфигурацию из переменных окружения.
func LoadConfig(log zerolog.Logger) Config {
	natsCfg := nc.DefaultConfig()
	natsCfg.Server.Host = utils.GetEnv(log, "NATS_HOST", natsCfg.Server.Host)
	natsCfg.Server.ClientPort = utils.GetEnv(log, "NATS_PORT", natsCfg.Server.ClientPort)
	natsCfg.Auth.User = utils.GetEnv(log, "NATS_USER", "")
	natsCfg.Auth.Password = utils.GetEnv(log, "NATS_PASSWORD", "")
	natsCfg.KV.BucketName = "" // xws не использует KV-хранилище.

	return Config{
		NATS:              natsCfg,
		InactivityTimeout: utils.GetEnv(log, "INACTIVITY_TIMEOUT", 3*time.Minute),
	}
}
