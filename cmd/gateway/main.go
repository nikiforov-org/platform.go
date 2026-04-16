// cmd/gateway/main.go
//
// Точка входа API Gateway.
// Вся логика вынесена в internal/services/gateway — здесь только инициализация
// зависимостей, запуск сервера и graceful shutdown.
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"platform/internal/platform/logger"
	"platform/internal/platform/nc"
	"platform/internal/services/gateway"
	"platform/utils"
)

func main() {
	log := logger.New("gateway")
	utils.SetLogger(log)

	// 1. Конфигурация из переменных окружения.
	cfg, err := gateway.LoadConfig()
	if err != nil {
		log.Fatal().Err(err).Msg("ошибка конфигурации")
	}

	if len(cfg.AllowedHosts) == 0 {
		log.Warn().Msg("ALLOWED_HOSTS не задан — проверка Origin отключена. Допустимо только в dev-окружении.")
	}

	// 2. NATS.
	natsClient, err := nc.NewClient(cfg.NATS, log)
	if err != nil {
		log.Fatal().Err(err).Msg("ошибка подключения к NATS")
	}

	// 3. Канал завершения — сигнализирует фоновым горутинам (rate limiter cleanup).
	stopCh := make(chan struct{})

	// 4. Gateway.
	gw := gateway.New(natsClient, cfg, log, stopCh)

	// 5. HTTP-сервер.
	server := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	// 6. Graceful Shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info().Str("addr", server.Addr).Msg("запущен")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("ошибка сервера")
		}
	}()

	<-stop
	log.Info().Msg("завершение работы...")
	close(stopCh) // останавливает фоновые горутины Gateway

	// Останавливаем HTTP: новые запросы не принимаются.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("ошибка HTTP Shutdown")
	}

	// Дренируем NATS: in-flight запросы завершаются, буфер сбрасывается.
	drainTimeout := utils.GetEnv("NATS_DRAIN_TIMEOUT", 15*time.Second)
	if err := natsClient.Drain(drainTimeout); err != nil {
		log.Error().Err(err).Msg("NATS drain")
	}
}
