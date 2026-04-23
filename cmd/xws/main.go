// cmd/xws/main.go
//
// Точка входа сервиса xws: WebSocket-сессии с таймаутом бездействия.
//
// Gateway открывает пару NATS-тем на каждую WS-сессию:
//
//	api.v1.xws.ws.in.{sid}   — входящий поток:  браузер → сервис
//	api.v1.xws.ws.out.{sid}  — исходящий поток: сервис  → браузер
//
// При WS-апгрейде gateway публикует connect-сообщение:
//
//	Subject: api.v1.xws.ws.connect
//	Header:  Sid={sid}, Cookie={raw cookie header}
//
// Queue Group гарантирует, что ровно один инстанс сервиса обслуживает каждую сессию.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"platform/internal/middleware"
	"platform/internal/platform/logger"
	"platform/internal/platform/nc"
	"platform/internal/services/xws"
	"platform/utils"

	"github.com/nats-io/nats.go"
)

// sid идёт в NATS-subject (`api.v1.xws.ws.in.<sid>` / `out.<sid>`):
// `.`, `*`, `>` и пробелы — спецсимволы маршрутизации NATS, при попадании
// в subject ломают подписку либо превращают её в wildcard. Gateway
// генерирует sid через CSPRNG (16 hex), но xws — отдельный сервис: любой
// другой компонент с NATS-доступом может опубликовать connect с произвольным
// sid. Defense-in-depth: валидируем формат на входе.
var validSID = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,64}$`)

func main() {
	log := logger.New("xws")
	cfg := xws.LoadConfig(log)

	healthAddr := os.Getenv("HEALTH_ADDR")
	if healthAddr == "" {
		log.Fatal().Msg("HEALTH_ADDR не задан")
	}

	natsClient, err := nc.NewClient(cfg.NATS, log)
	if err != nil {
		log.Fatal().Err(err).Msg("NATS")
	}

	healthSrv, err := natsClient.RegisterHealth("xws", healthAddr)
	if err != nil {
		log.Fatal().Err(err).Msg("health")
	}

	mgr := xws.NewManager(natsClient.Conn, cfg.InactivityTimeout, log)

	// Управляющая подписка: gateway сигнализирует о новой WS-сессии.
	// Queue Group гарантирует, что одну сессию возьмёт ровно один инстанс.
	const (
		connectSubject = "api.v1.xws.ws.connect"
		queue          = "xws"
	)

	_, err = natsClient.Conn.QueueSubscribe(connectSubject, queue, middleware.Recover(log, func(msg *nats.Msg) {
		sid := msg.Header.Get("Sid")
		if sid == "" {
			// Fallback для совместимости: SID может прийти в теле сообщения.
			var req struct {
				SID string `json:"sid"`
			}
			if jsonErr := json.Unmarshal(msg.Data, &req); jsonErr != nil || req.SID == "" {
				log.Warn().Msg("невалидный connect payload")
				return
			}
			sid = req.SID
		}
		if !validSID.MatchString(sid) {
			log.Warn().Str("sid", sid).Msg("невалидный формат sid, отклонён")
			return
		}
		mgr.Open(sid)
		// Ack для Gateway: подтверждаем регистрацию сессии. Без ack Gateway
		// закроет WS-соединение по таймауту (GATEWAY_WS_CONNECT_TIMEOUT).
		if err := msg.Respond(nil); err != nil {
			log.Warn().Err(err).Str("sid", sid).Msg("connect ack failed")
		}
	}))
	if err != nil {
		log.Fatal().Err(err).Str("subject", connectSubject).Msg("QueueSubscribe")
	}

	log.Info().Str("subject", connectSubject).Str("queue", queue).Dur("inactivity_timeout", cfg.InactivityTimeout).Msg("запущен")

	// Ожидание сигнала завершения.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Info().Msg("завершение работы...")
	// Порядок: /healthz → WS-сессии → NATS drain.
	// Сначала Nomad снимает аллокацию с балансировки через /healthz 503,
	// затем закрываем WS-сессии (отписка + Control: CLOSE клиентам),
	// затем дренируем оставшиеся бизнес-подписки.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := healthSrv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("health shutdown")
	}
	cancel()

	mgr.CloseAll()
	drainTimeout := utils.GetEnv(log, "NATS_DRAIN_TIMEOUT", 15*time.Second)
	if err := natsClient.Drain(drainTimeout); err != nil {
		log.Error().Err(err).Msg("NATS drain")
	}
}
