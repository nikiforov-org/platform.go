// cmd/gateway/main.go
//
// Точка входа API Gateway.
// Вся логика вынесена в internal/services/gateway — здесь только инициализация
// зависимостей, запуск сервера и graceful shutdown.
package main

import (
	"context"
	"net"
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

	// 1. Конфигурация из переменных окружения.
	cfg, err := gateway.LoadConfig(log)
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
	// BaseContext — чтобы r.Context() каждого входящего запроса отменялся при
	// graceful shutdown. Это позволяет NATS-вызовам (RequestMsgWithContext)
	// и WS-обработчикам мгновенно реагировать на shutdown, а не ждать таймаута.
	server := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		BaseContext:       func(net.Listener) context.Context { return gw.RootContext() },
	}

	// 6. Graceful Shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// Ошибка сервера направляется в канал, а не в log.Fatal — иначе os.Exit(1)
	// в горутине обошёл бы close(stopCh), server.Shutdown и NATS Drain ниже,
	// потеряв in-flight запросы и оставив WS-сессии без close-фреймов.
	serverErr := make(chan error, 1)
	go func() {
		log.Info().Str("addr", server.Addr).Msg("запущен")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	var exitCode int
	select {
	case <-stop:
		log.Info().Msg("завершение работы...")
	case err := <-serverErr:
		log.Error().Err(err).Msg("ошибка сервера, инициируем shutdown")
		exitCode = 1
	}
	close(stopCh) // останавливает фоновые горутины Gateway

	// Останавливаем HTTP: новые запросы не принимаются.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("ошибка HTTP Shutdown")
	}
	cancel() // явно — defer не сработает после os.Exit ниже

	// Дренируем NATS: in-flight запросы завершаются, буфер сбрасывается.
	drainTimeout := utils.GetEnv(log, "NATS_DRAIN_TIMEOUT", 15*time.Second)
	if err := natsClient.Drain(drainTimeout); err != nil {
		log.Error().Err(err).Msg("NATS drain")
	}

	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
