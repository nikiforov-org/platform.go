// internal/services/xws/config.go
package xws

import (
	"time"

	"platform/internal/platform/natsclient"
	"platform/utils"
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
	NATS              natsclient.Config
	InactivityTimeout time.Duration
}

// LoadConfig читает конфигурацию из переменных окружения.
func LoadConfig() Config {
	natsCfg := natsclient.DefaultConfig()
	natsCfg.Server.Host = utils.GetEnv("NATS_HOST", natsCfg.Server.Host)
	natsCfg.Server.ClientPort = utils.GetEnv("NATS_PORT", natsCfg.Server.ClientPort)
	natsCfg.Auth.User = utils.GetEnv("NATS_USER", "")
	natsCfg.Auth.Password = utils.GetEnv("NATS_PASSWORD", "")
	natsCfg.KV.BucketName = "" // xws не использует KV-хранилище.

	return Config{
		NATS:              natsCfg,
		InactivityTimeout: utils.GetEnv("INACTIVITY_TIMEOUT", 3*time.Minute),
	}
}
