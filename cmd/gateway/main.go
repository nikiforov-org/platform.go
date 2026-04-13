// cmd/gateway/main.go
//
// Точка входа API Gateway.
// Вся логика вынесена в internal/services/gateway — здесь только инициализация
// зависимостей, запуск сервера и graceful shutdown.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"platform/internal/platform/natsclient"
	"platform/internal/services/gateway"
)

func main() {
	// 1. Конфигурация из переменных окружения.
	cfg, err := gateway.LoadConfig()
	if err != nil {
		log.Fatalf("gateway: ошибка конфигурации: %v", err)
	}

	if len(cfg.AllowedHosts) == 0 {
		log.Println("gateway: WARN: ALLOWED_HOSTS не задан — проверка Origin отключена. Допустимо только в dev-окружении.")
	}

	// 2. NATS.
	nc, err := natsclient.NewClient(cfg.NATS)
	if err != nil {
		log.Fatalf("gateway: ошибка подключения к NATS: %v", err)
	}
	defer nc.Close()

	// 3. Gateway.
	gw := gateway.New(nc, cfg.AllowedHosts)

	// 4. HTTP-сервер.
	server := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	// 5. Graceful Shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("gateway: запущен на %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("gateway: ошибка сервера: %v", err)
		}
	}()

	<-stop
	log.Println("gateway: завершение работы...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("gateway: ошибка при Shutdown: %v", err)
	}
}
