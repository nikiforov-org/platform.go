// cmd/xhttp/main.go
//
// Точка входа сервиса xhttp: CRUD поверх PostgreSQL с KV-кэшем.
//
// Subjects (Queue Group "xhttp"):
//
//	api.v1.xhttp.create  — создать запись         [защищён RequireAuth]
//	api.v1.xhttp.get     — получить по ID         [публичный]
//	api.v1.xhttp.list    — список всех записей    [публичный]
//	api.v1.xhttp.update  — обновить по ID         [защищён RequireAuth]
//	api.v1.xhttp.delete  — удалить по ID          [защищён RequireAuth]
package main

import (
	"database/sql"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"platform/internal/middleware"
	"platform/internal/platform/natsclient"
	"platform/internal/services/xhttp"

	"github.com/nats-io/nats.go"
)

func main() {
	cfg := xhttp.LoadConfig()

	// 1. PostgreSQL.
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("xhttp: sql.Open: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		log.Fatalf("xhttp: db.Ping: %v", err)
	}
	log.Println("xhttp: PostgreSQL подключена")

	if err := xhttp.Migrate(db); err != nil {
		log.Fatalf("xhttp: migrate: %v", err)
	}
	log.Println("xhttp: миграция выполнена")

	// 2. NATS.
	nc, err := natsclient.NewClient(cfg.NATS)
	if err != nil {
		log.Fatalf("xhttp: NATS: %v", err)
	}
	defer nc.Close()

	h := xhttp.NewHandlers(nc, db, cfg)

	// 3. Конфигурация middleware для проверки JWT.
	// ACCESS_SECRET должен совпадать с AUTH_ACCESS_SECRET сервиса auth-ms.
	authCfg := middleware.AuthConfig{
		AccessSecret: []byte(os.Getenv("ACCESS_SECRET")),
	}

	// 4. Регистрация подписок.
	// RequireAuth — опциональная обёртка: применяется только там, где нужна авторизация.
	// HandleList намеренно оставлен публичным — позволяет читать данные без токена.
	const queue = "xhttp"
	subs := []struct {
		subject string
		handler nats.MsgHandler
	}{
		{"api.v1.xhttp.create", middleware.RequireAuth(authCfg, h.HandleCreate)},
		{"api.v1.xhttp.get", h.HandleGet},   // публичный эндпоинт
		{"api.v1.xhttp.list", h.HandleList}, // публичный эндпоинт
		{"api.v1.xhttp.update", middleware.RequireAuth(authCfg, h.HandleUpdate)},
		{"api.v1.xhttp.delete", middleware.RequireAuth(authCfg, h.HandleDelete)},
	}

	for _, s := range subs {
		if _, err := nc.Conn.QueueSubscribe(s.subject, queue, s.handler); err != nil {
			log.Fatalf("xhttp: QueueSubscribe %s: %v", s.subject, err)
		}
		log.Printf("xhttp: подписан на %s [queue: %s]", s.subject, queue)
	}

	// 5. Ожидание сигнала завершения.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("xhttp: завершение работы...")
}
