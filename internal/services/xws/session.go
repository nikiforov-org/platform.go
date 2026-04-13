// internal/services/xws/session.go
package xws

import (
	"encoding/json"
	"log"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// InMsg — входящее сообщение от браузера.
type InMsg struct {
	// Type — тип сообщения: "ping", "message", "disconnect".
	Type string `json:"type"`
	// Text — произвольный текст (для type="message").
	Text string `json:"text,omitempty"`
}

// OutMsg — исходящее сообщение браузеру.
type OutMsg struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	SID  string `json:"sid,omitempty"`
}

// session — активная WebSocket-сессия одного клиента.
type session struct {
	sid     string
	outSubj string // api.v1.xws.ws.out.{sid}
	inSub   *natsgo.Subscription
	timer   *time.Timer // таймер бездействия; сбрасывается при каждом сообщении
	nc      *natsgo.Conn
	timeout time.Duration
}

// send публикует OutMsg в исходящую NATS-тему сессии.
func (s *session) send(out OutMsg) {
	body, err := json.Marshal(out)
	if err != nil {
		log.Printf("xws: [sid:%s] marshal error: %v", s.sid, err)
		return
	}
	if err := s.nc.Publish(s.outSubj, body); err != nil {
		log.Printf("xws: [sid:%s] publish error: %v", s.sid, err)
	}
}

// close публикует управляющий фрейм Control=CLOSE и отписывается от входящей темы.
// Gateway получает фрейм и закрывает WebSocket-соединение со стороны сервера.
func (s *session) close() {
	msg := natsgo.NewMsg(s.outSubj)
	msg.Header.Set("Control", "CLOSE")
	if err := s.nc.PublishMsg(msg); err != nil {
		log.Printf("xws: [sid:%s] ошибка отправки CLOSE: %v", s.sid, err)
	}
	if err := s.inSub.Unsubscribe(); err != nil {
		log.Printf("xws: [sid:%s] ошибка Unsubscribe: %v", s.sid, err)
	}
	log.Printf("xws: [sid:%s] сессия закрыта", s.sid)
}

// resetTimer сбрасывает таймер бездействия при получении любого сообщения.
func (s *session) resetTimer() {
	s.timer.Reset(s.timeout)
}
