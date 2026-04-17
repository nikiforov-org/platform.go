// internal/services/xws/session.go
package xws

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
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
//
// mu синхронизирует close()/resetTimer() с AfterFunc-коллбэком таймера:
// без него Reset() мог бы стрелять параллельно с уже запущенным
// коллбэком (Go docs прямо это запрещают), а disconnect-ветка и таймер —
// одновременно вызвать Unsubscribe + PublishMsg(CLOSE) дважды.
type session struct {
	mu      sync.Mutex
	closed  bool
	sid     string
	outSubj string // api.v1.xws.ws.out.{sid}
	inSub   *nats.Subscription
	timer   *time.Timer // таймер бездействия; сбрасывается при каждом сообщении
	nc      *nats.Conn
	timeout time.Duration
	log     zerolog.Logger
}

// send публикует OutMsg в исходящую NATS-тему сессии.
func (s *session) send(out OutMsg) {
	body, err := json.Marshal(out)
	if err != nil {
		s.log.Error().Err(err).Str("sid", s.sid).Msg("marshal error")
		return
	}
	if err := s.nc.Publish(s.outSubj, body); err != nil {
		s.log.Error().Err(err).Str("sid", s.sid).Msg("publish error")
	}
}

// close публикует управляющий фрейм Control=CLOSE и отписывается от входящей темы.
// Gateway получает фрейм и закрывает WebSocket-соединение со стороны сервера.
// Идемпотентен: повторные вызовы — no-op.
func (s *session) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.timer.Stop()
	s.mu.Unlock()

	msg := nats.NewMsg(s.outSubj)
	msg.Header.Set("Control", "CLOSE")
	if err := s.nc.PublishMsg(msg); err != nil {
		s.log.Error().Err(err).Str("sid", s.sid).Msg("ошибка отправки CLOSE")
	}
	if err := s.inSub.Unsubscribe(); err != nil {
		s.log.Error().Err(err).Str("sid", s.sid).Msg("ошибка Unsubscribe")
	}
	s.log.Info().Str("sid", s.sid).Msg("сессия закрыта")
}

// resetTimer сбрасывает таймер бездействия при получении любого сообщения.
// После close() — no-op, чтобы не возникала гонка Reset vs AfterFunc.
func (s *session) resetTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.timer.Reset(s.timeout)
}
