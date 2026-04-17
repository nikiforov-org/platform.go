// cmd/xauth/main.go
//
// Точка входа сервиса xauth: JWT-авторизация через HttpOnly-куки.
//
// Subjects (Queue Group "xauth"):
//
//	api.v1.xauth.login    — выдача access + refresh токенов
//	api.v1.xauth.refresh  — ротация токенов по refresh-куке
//	api.v1.xauth.logout   — отзыв refresh-токена, очистка кук
//	api.v1.xauth.me       — проверка access-токена, возврат claims
package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"platform/internal/middleware"
	"platform/internal/platform/logger"
	"platform/internal/platform/nc"
	"platform/internal/services/xauth"
	"platform/utils"

	"github.com/nats-io/nats.go"
)

func main() {
	log := logger.New("xauth")
	cfg := xauth.LoadConfig(log)

	natsClient, err := nc.NewClient(cfg.NATS, log)
	if err != nil {
		log.Fatal().Err(err).Msg("NATS")
	}

	h := xauth.NewHandlers(natsClient, cfg, log)

	// Все эндпоинты xauth публичны по определению —
	// именно этот сервис выдаёт токены, а не проверяет их.
	// Проверка токенов — задача middleware в других сервисах.
	const queue = "xauth"
	subs := []struct {
		subject string
		handler nats.MsgHandler
	}{
		{"api.v1.xauth.login", middleware.Recover(log, h.HandleLogin)},
		{"api.v1.xauth.refresh", middleware.Recover(log, h.HandleRefresh)},
		{"api.v1.xauth.logout", middleware.Recover(log, h.HandleLogout)},
		{"api.v1.xauth.me", middleware.Recover(log, h.HandleMe)},
	}

	for _, s := range subs {
		if _, err := natsClient.Conn.QueueSubscribe(s.subject, queue, s.handler); err != nil {
			log.Fatal().Err(err).Str("subject", s.subject).Msg("QueueSubscribe")
		}
		log.Info().Str("subject", s.subject).Str("queue", queue).Msg("подписан")
	}

	log.Info().Str("access_ttl", cfg.AccessTTL.String()).Str("refresh_ttl", cfg.RefreshTTL.String()).Msg("запущен")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Info().Msg("завершение работы...")
	drainTimeout := utils.GetEnv(log, "NATS_DRAIN_TIMEOUT", 15*time.Second)
	if err := natsClient.Drain(drainTimeout); err != nil {
		log.Error().Err(err).Msg("NATS drain")
	}
}
