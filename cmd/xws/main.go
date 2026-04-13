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
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"

	"platform/internal/platform/natsclient"
	"platform/internal/services/xws"

	"github.com/nats-io/nats.go"
)

func main() {
	cfg := xws.LoadConfig()

	nc, err := natsclient.NewClient(cfg.NATS)
	if err != nil {
		log.Fatalf("xws: NATS: %v", err)
	}
	defer nc.Close()

	mgr := xws.NewManager(nc.Conn, cfg.InactivityTimeout)

	// Управляющая подписка: gateway сигнализирует о новой WS-сессии.
	// Queue Group гарантирует, что одну сессию возьмёт ровно один инстанс.
	const (
		connectSubject = "api.v1.xws.ws.connect"
		queue          = "xws"
	)

	_, err = nc.Conn.QueueSubscribe(connectSubject, queue, func(msg *nats.Msg) {
		sid := msg.Header.Get("Sid")
		if sid == "" {
			// Fallback для совместимости: SID может прийти в теле сообщения.
			var req struct {
				SID string `json:"sid"`
			}
			if jsonErr := json.Unmarshal(msg.Data, &req); jsonErr != nil || req.SID == "" {
				log.Printf("xws: невалидный connect payload")
				return
			}
			sid = req.SID
		}
		mgr.Open(sid)
	})
	if err != nil {
		log.Fatalf("xws: QueueSubscribe %s: %v", connectSubject, err)
	}

	log.Printf("xws: ожидание сессий на %s [queue: %s]", connectSubject, queue)
	log.Printf("xws: таймаут бездействия: %s", cfg.InactivityTimeout)

	// Ожидание сигнала завершения.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("xws: завершение работы...")
	mgr.CloseAll()
}
