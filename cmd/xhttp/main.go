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
	"os"
	"os/signal"
	"syscall"
	"time"

	"platform/internal/middleware"
	"platform/internal/platform/logger"
	"platform/internal/platform/nc"
	"platform/internal/services/xhttp"
	"platform/utils"

	_ "github.com/lib/pq" // PostgreSQL-драйвер для database/sql

	"github.com/nats-io/nats.go"
)

func main() {
	log := logger.New("xhttp")
	cfg := xhttp.LoadConfig(log)

	// 1. PostgreSQL.
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("sql.Open")
	}
	defer db.Close()

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		log.Fatal().Err(err).Msg("db.Ping")
	}
	log.Info().Msg("PostgreSQL подключена")

	if err := xhttp.Migrate(db); err != nil {
		log.Fatal().Err(err).Msg("migrate")
	}
	log.Info().Msg("миграция выполнена")

	// 2. NATS.
	natsClient, err := nc.NewClient(cfg.NATS, log)
	if err != nil {
		log.Fatal().Err(err).Msg("NATS")
	}

	h := xhttp.NewHandlers(natsClient, db, cfg, log)

	// 3. Конфигурация middleware для проверки JWT.
	// ACCESS_SECRET должен совпадать с AUTH_ACCESS_SECRET сервиса xauth.
	// Fail-fast при пустом значении: иначе HMAC-проверка с пустым ключом
	// пропустит любой токен, подписанный таким же пустым ключом.
	accessSecret := os.Getenv("ACCESS_SECRET")
	if accessSecret == "" {
		log.Fatal().Msg("ACCESS_SECRET обязательна, должна совпадать с AUTH_ACCESS_SECRET сервиса xauth")
	}
	authCfg := middleware.AuthConfig{
		AccessSecret: []byte(accessSecret),
		Log:          log,
	}

	// 4. Регистрация подписок.
	// RequireAuth — опциональная обёртка: применяется только там, где нужна авторизация.
	// HandleList намеренно оставлен публичным — позволяет читать данные без токена.
	const queue = "xhttp"
	subs := []struct {
		subject string
		handler nats.MsgHandler
	}{
		{"api.v1.xhttp.create", middleware.Recover(log, middleware.RequireAuth(authCfg, h.HandleCreate))},
		{"api.v1.xhttp.get", middleware.Recover(log, h.HandleGet)},   // публичный эндпоинт
		{"api.v1.xhttp.list", middleware.Recover(log, h.HandleList)}, // публичный эндпоинт
		{"api.v1.xhttp.update", middleware.Recover(log, middleware.RequireAuth(authCfg, h.HandleUpdate))},
		{"api.v1.xhttp.delete", middleware.Recover(log, middleware.RequireAuth(authCfg, h.HandleDelete))},
	}

	for _, s := range subs {
		if _, err := natsClient.Conn.QueueSubscribe(s.subject, queue, s.handler); err != nil {
			log.Fatal().Err(err).Str("subject", s.subject).Msg("QueueSubscribe")
		}
		log.Info().Str("subject", s.subject).Str("queue", queue).Msg("подписан")
	}

	// 5. Ожидание сигнала завершения.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Info().Msg("завершение работы...")

	// Сначала дренируем NATS: in-flight обработчики завершают работу,
	// новые сообщения не принимаются — новые DB-запросы не стартуют.
	drainTimeout := utils.GetEnv(log, "NATS_DRAIN_TIMEOUT", 15*time.Second)
	if err := natsClient.Drain(drainTimeout); err != nil {
		log.Error().Err(err).Msg("NATS drain")
	}
	// db.Close() вызывается через defer выше, после возврата из main.
}
