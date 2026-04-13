// internal/services/xws/manager.go
package xws

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// Manager — реестр активных WS-сессий.
// Потокобезопасен: несколько горутин (NATS-коллбэки, таймеры) могут
// одновременно открывать, закрывать и читать сессии.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*session
	nc       *natsgo.Conn
	timeout  time.Duration
}

// NewManager создаёт экземпляр Manager с заданным таймаутом бездействия.
func NewManager(nc *natsgo.Conn, timeout time.Duration) *Manager {
	return &Manager{
		sessions: make(map[string]*session),
		nc:       nc,
		timeout:  timeout,
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
		log.Printf("xws: [sid:%s] сессия уже существует, пропускаем", sid)
		return
	}

	inSubj := "api.v1.xws.ws.in." + sid
	outSubj := "api.v1.xws.ws.out." + sid

	sess := &session{
		sid:     sid,
		outSubj: outSubj,
		nc:      m.nc,
		timeout: m.timeout,
	}

	// Таймер бездействия: по истечении публикуем CLOSE и удаляем сессию.
	sess.timer = time.AfterFunc(m.timeout, func() {
		log.Printf("xws: [sid:%s] таймаут бездействия (%s)", sid, m.timeout)
		sess.close()
		m.remove(sid)
	})

	// Каждая сессия — уникальная тема, Queue Group не нужна:
	// конкретный SID должен обрабатываться одним инстансом, который его открыл.
	sub, err := m.nc.Subscribe(inSubj, func(msg *natsgo.Msg) {
		m.handleIncoming(sess, msg)
	})
	if err != nil {
		sess.timer.Stop()
		log.Printf("xws: [sid:%s] ошибка Subscribe: %v", sid, err)
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

	log.Printf("xws: [sid:%s] сессия открыта (timeout: %s)", sid, m.timeout)
}

// handleIncoming обрабатывает входящее сообщение от браузера.
// Любое сообщение сбрасывает таймер бездействия.
func (m *Manager) handleIncoming(sess *session, msg *natsgo.Msg) {
	// Любое входящее сообщение — признак активности клиента.
	sess.resetTimer()

	var in InMsg
	if err := json.Unmarshal(msg.Data, &in); err != nil {
		log.Printf("xws: [sid:%s] невалидный JSON: %v", sess.sid, err)
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
		log.Printf("xws: [sid:%s] клиент запросил disconnect", sess.sid)
		sess.timer.Stop()
		sess.close()
		m.remove(sess.sid)

	default:
		log.Printf("xws: [sid:%s] неизвестный тип: %q", sess.sid, in.Type)
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
	log.Println("xws: все сессии закрыты")
}
