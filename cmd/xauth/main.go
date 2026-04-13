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
	"log"
	"os"
	"os/signal"
	"syscall"

	"platform/internal/platform/natsclient"
	"platform/internal/services/xauth"

	"github.com/nats-io/nats.go"
)

func main() {
	cfg := xauth.LoadConfig()

	nc, err := natsclient.NewClient(cfg.NATS)
	if err != nil {
		log.Fatalf("xauth: NATS: %v", err)
	}
	defer nc.Close()

	h := xauth.NewHandlers(nc, cfg)

	// Все эндпоинты xauth публичны по определению —
	// именно этот сервис выдаёт токены, а не проверяет их.
	// Проверка токенов — задача middleware в других сервисах.
	const queue = "xauth"
	subs := []struct {
		subject string
		handler nats.MsgHandler
	}{
		{"api.v1.xauth.login", h.HandleLogin},
		{"api.v1.xauth.refresh", h.HandleRefresh},
		{"api.v1.xauth.logout", h.HandleLogout},
		{"api.v1.xauth.me", h.HandleMe},
	}

	for _, s := range subs {
		if _, err := nc.Conn.QueueSubscribe(s.subject, queue, s.handler); err != nil {
			log.Fatalf("xauth: QueueSubscribe %s: %v", s.subject, err)
		}
		log.Printf("xauth: подписан на %s [queue: %s]", s.subject, queue)
	}

	log.Printf("xauth: access TTL=%s, refresh TTL=%s", cfg.AccessTTL, cfg.RefreshTTL)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("xauth: завершение работы...")
}
