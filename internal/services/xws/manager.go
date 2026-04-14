// internal/services/xws/manager.go
package xws

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// Manager — реестр активных WS-сессий.
// Потокобезопасен: несколько горутин (NATS-коллбэки, таймеры) могут
// одновременно открывать, закрывать и читать сессии.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*session
	nc       *nats.Conn
	timeout  time.Duration
	log      zerolog.Logger
}

// NewManager создаёт экземпляр Manager с заданным таймаутом бездействия.
func NewManager(nc *nats.Conn, timeout time.Duration, log zerolog.Logger) *Manager {
	return &Manager{
		sessions: make(map[string]*session),
		nc:       nc,
		timeout:  timeout,
		log:      log,
	}
}

// Open регистрирует новую сессию по SID из connect-сообщения gateway,
// подписывается на входящий поток и запускает таймер бездействия.
//
// Если сессия с таким SID уже существует — вызов игнорируется.
// Это защищает от повторной доставки connect-сообщения при реконнекте NATS.
func (m *Manager) Open(sid string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[sid]; exists {
		m.log.Warn().Str("sid", sid).Msg("сессия уже существует, пропускаем")
		return
	}

	inSubj := "api.v1.xws.ws.in." + sid
	outSubj := "api.v1.xws.ws.out." + sid

	sess := &session{
		sid:     sid,
		outSubj: outSubj,
		nc:      m.nc,
		timeout: m.timeout,
		log:     m.log,
	}

	// Таймер бездействия: по истечении публикуем CLOSE и удаляем сессию.
	sess.timer = time.AfterFunc(m.timeout, func() {
		m.log.Info().Str("sid", sid).Dur("timeout", m.timeout).Msg("таймаут бездействия")
		sess.close()
		m.remove(sid)
	})

	// Каждая сессия — уникальная тема, Queue Group не нужна:
	// конкретный SID должен обрабатываться одним инстансом, который его открыл.
	sub, err := m.nc.Subscribe(inSubj, func(msg *nats.Msg) {
		m.handleIncoming(sess, msg)
	})
	if err != nil {
		sess.timer.Stop()
		m.log.Error().Err(err).Str("sid", sid).Msg("ошибка Subscribe")
		return
	}
	sess.inSub = sub
	m.sessions[sid] = sess

	// Подтверждаем соединение клиенту.
	sess.send(OutMsg{
		Type: "connected",
		SID:  sid,
		Text: "Соединение будет закрыто после " + m.timeout.String() + " бездействия.",
	})

	m.log.Info().Str("sid", sid).Dur("timeout", m.timeout).Msg("сессия открыта")
}

// handleIncoming обрабатывает входящее сообщение от браузера.
// Любое сообщение сбрасывает таймер бездействия.
func (m *Manager) handleIncoming(sess *session, msg *nats.Msg) {
	// Любое входящее сообщение — признак активности клиента.
	sess.resetTimer()

	var in InMsg
	if err := json.Unmarshal(msg.Data, &in); err != nil {
		m.log.Warn().Err(err).Str("sid", sess.sid).Msg("невалидный JSON")
		return
	}

	switch in.Type {
	case "ping":
		// Heartbeat от клиента — подтверждаем живость соединения.
		sess.send(OutMsg{Type: "pong"})

	case "message":
		// Эхо-ответ. Здесь располагается бизнес-логика сервиса.
		sess.send(OutMsg{Type: "message", Text: "echo: " + in.Text})

	case "disconnect":
		// Клиент явно запросил закрытие — не ждём таймаута.
		m.log.Info().Str("sid", sess.sid).Msg("клиент запросил disconnect")
		sess.timer.Stop()
		sess.close()
		m.remove(sess.sid)

	default:
		m.log.Warn().Str("sid", sess.sid).Str("type", in.Type).Msg("неизвестный тип сообщения")
	}
}

// remove удаляет сессию из реестра.
func (m *Manager) remove(sid string) {
	m.mu.Lock()
	delete(m.sessions, sid)
	m.mu.Unlock()
}

// CloseAll завершает все активные сессии.
// Вызывается при остановке сервиса (SIGTERM) — браузеры получают CLOSE
// и не зависают с открытым соединением.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for sid, sess := range m.sessions {
		sess.timer.Stop()
		sess.close()
		delete(m.sessions, sid)
	}
	m.log.Info().Msg("все сессии закрыты")
}
