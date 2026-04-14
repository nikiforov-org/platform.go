// internal/services/xauth/config.go
package xauth

import (
	"os"
	"time"

	"platform/internal/platform/nc"
	"platform/utils"
)

// Config — конфигурация сервиса xauth.
//
// Переменные окружения:
//
//	NATS_HOST            — хост NATS-сервера                ("127.0.0.1")
//	NATS_PORT            — порт NATS-сервера                 (4222)
//	NATS_USER            — логин авторизации                 ("")
//	NATS_PASSWORD        — пароль авторизации                ("")
//	NATS_KV_REPLICAS     — число реплик KV-бакета            (3; для dev: 1)
//	AUTH_USERNAME        — логин пользователя                (обязательно)
//	AUTH_PASSWORD        — пароль пользователя               (обязательно)
//	AUTH_ACCESS_SECRET   — HMAC-секрет для access-токенов   (обязательно)
//	AUTH_REFRESH_SECRET  — HMAC-секрет для refresh-токенов  (обязательно)
//	AUTH_ACCESS_TTL      — время жизни access-токена         ("15m")
//	AUTH_REFRESH_TTL     — время жизни refresh-токена        ("168h")
//	COOKIE_DOMAIN        — домен кук                         ("")
//	COOKIE_SECURE        — флаг Secure на куках              ("true")
type Config struct {
	NATS          nc.Config
	Username      string
	Password      string
	AccessSecret  []byte
	RefreshSecret []byte
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
	CookieDomain  string
	CookieSecure  bool
}

// LoadConfig читает конфигурацию из переменных окружения.
// Завершает процесс с ошибкой, если обязательные переменные не заданы.
func LoadConfig() Config {
	mustEnv := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			log := utils.Logger()
			log.Fatal().Str("key", key).Msg("обязательная переменная окружения не задана")
		}
		return v
	}

	natsCfg := nc.DefaultConfig()
	natsCfg.Server.Host = utils.GetEnv("NATS_HOST", natsCfg.Server.Host)
	natsCfg.Server.ClientPort = utils.GetEnv("NATS_PORT", natsCfg.Server.ClientPort)
	natsCfg.Auth.User = utils.GetEnv("NATS_USER", "")
	natsCfg.Auth.Password = utils.GetEnv("NATS_PASSWORD", "")
	// KV-бакет для хранения JTI refresh-токенов (для отзыва при logout/ротации).
	natsCfg.KV.BucketName = "authms_refresh_tokens"
	natsCfg.KV.Replicas = utils.GetEnv("NATS_KV_REPLICAS", natsCfg.KV.Replicas)
	natsCfg.KV.History = 1

	return Config{
		NATS:          natsCfg,
		Username:      mustEnv("AUTH_USERNAME"),
		Password:      mustEnv("AUTH_PASSWORD"),
		AccessSecret:  []byte(mustEnv("AUTH_ACCESS_SECRET")),
		RefreshSecret: []byte(mustEnv("AUTH_REFRESH_SECRET")),
		AccessTTL:     utils.GetEnv("AUTH_ACCESS_TTL", 15*time.Minute),
		RefreshTTL:    utils.GetEnv("AUTH_REFRESH_TTL", 168*time.Hour),
		CookieDomain:  utils.GetEnv("COOKIE_DOMAIN", ""),
		CookieSecure:  utils.GetEnv("COOKIE_SECURE", true),
	}
}
