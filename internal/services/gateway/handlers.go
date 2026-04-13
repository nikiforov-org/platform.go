// internal/services/gateway/handlers.go
package gateway

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"platform/internal/platform/natsclient"
	"platform/utils"

	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"
)

// =============================================================================
// Константы WebSocket
// =============================================================================

// wsWriteDeadline — дедлайн на запись в WebSocket-соединение.
// Предотвращает блокировку горутины при медленном или зависшем клиенте.
const wsWriteDeadline = 10 * time.Second

// wsReadDeadline — максимальное время ожидания данных или Pong от клиента.
// Если за это время ничего не получено — соединение считается мёртвым.
const wsReadDeadline = 60 * time.Second

// wsPingInterval — интервал отправки Ping-фреймов клиенту.
// Должен быть меньше wsReadDeadline, чтобы Pong успел вернуться вовремя.
const wsPingInterval = 30 * time.Second

// =============================================================================
// Gateway
// =============================================================================

// Gateway — HTTP/WebSocket-шлюз, транслирующий запросы в NATS Request-Reply.
type Gateway struct {
	nats         *natsclient.PlatformClient
	upgrader     websocket.Upgrader
	allowedHosts utils.AllowedHostSet
}

// New создаёт Gateway с переданными зависимостями.
// CheckOrigin делегируется allowedHosts — HTTP и WebSocket используют одно правило.
func New(nc *natsclient.PlatformClient, allowedHosts utils.AllowedHostSet) *Gateway {
	gw := &Gateway{
		nats:         nc,
		allowedHosts: allowedHosts,
	}
	gw.upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return allowedHosts.Allows(r.Header.Get("Origin"))
		},
	}
	return gw
}

// Handler возвращает корневой http.Handler шлюза.
// Применяет middleware Origin поверх маршрутизатора /v1/.
func (gw *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/", gw.route)
	return gw.middlewareOrigin(mux)
}

// =============================================================================
// Маршрутизация
// =============================================================================

// middlewareOrigin проверяет заголовок Origin для входящих HTTP-запросов.
//
// WebSocket-соединения дополнительно проверяются через upgrader.CheckOrigin,
// но middleware перехватывает запрос раньше апгрейда — невалидный Origin
// не доходит до логики WS-обработчика.
//
// Запросы без Origin (curl, серверные вызовы, health checks) пропускаются:
// Origin шлют только браузеры при кросс-доменных запросах.
func (gw *Gateway) middlewareOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !gw.allowedHosts.Allows(origin) {
			log.Printf("gateway: отклонён Origin %q [%s %s]", origin, r.Method, r.URL.Path)
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// route — маршрутизатор запросов внутри /v1/.
// Паттерн: /v1/{service}/{method...} или /v1/{service}/ws
func (gw *Gateway) route(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")

	if len(parts) < 2 || parts[0] != "v1" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	service := parts[1]
	if service == "" {
		http.Error(w, "service name is required", http.StatusBadRequest)
		return
	}

	if parts[len(parts)-1] == "ws" {
		gw.handleWS(w, r, service)
		return
	}

	gw.handleHTTP(w, r, service, parts[2:])
}

// =============================================================================
// HTTP RPC
// =============================================================================

// handleHTTP проксирует HTTP-запрос в NATS по паттерну Request-Reply.
// Тема формируется как: api.v1.{service}.{method.submethod...}
//
// Проброс заголовков запрос → NATS:
//   - X-Real-IP     — адрес клиента
//   - Authorization — Bearer-токен, если присутствует
//   - Cookie        — все куки клиента (используются микросервисами для auth)
//
// Проброс заголовков NATS → ответ:
//   - Status    — управляет HTTP-кодом ответа клиенту; не копируется как заголовок.
//   - Set-Cookie — добавляется через Add, а не Set: каждая кука — отдельная строка.
//   - Остальные — копируются первым значением через Set.
func (gw *Gateway) handleHTTP(w http.ResponseWriter, r *http.Request, service string, methodParts []string) {
	method := strings.Join(methodParts, ".")
	subject := fmt.Sprintf("api.v1.%s.%s", service, method)

	// Ограничиваем чтение тела 1 МБ для защиты от злоупотреблений.
	const maxBodySize = 1 << 20 // 1 MB
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
	defer r.Body.Close()
	if err != nil {
		log.Printf("gateway: ошибка чтения тела [%s]: %v", subject, err)
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	msg := nats.NewMsg(subject)
	msg.Data = body
	msg.Header.Set("X-Real-IP", r.RemoteAddr)

	if auth := r.Header.Get("Authorization"); auth != "" {
		msg.Header.Set("Authorization", auth)
	}
	if cookie := r.Header.Get("Cookie"); cookie != "" {
		msg.Header.Set("Cookie", cookie)
	}

	resp, err := gw.nats.Conn.RequestMsg(msg, 5*time.Second)
	if err != nil {
		log.Printf("gateway: NATS request error [%s]: %v", subject, err)
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	statusCode := http.StatusOK
	for k, values := range resp.Header {
		switch k {
		case "Status":
			if len(values) > 0 {
				if n := parseStatus(values[0]); n != 0 {
					statusCode = n
				}
			}
		case "Set-Cookie":
			// Каждая кука — отдельный заголовок; Add не перезаписывает предыдущие.
			for _, v := range values {
				w.Header().Add("Set-Cookie", v)
			}
		default:
			if len(values) > 0 {
				w.Header().Set(k, values[0])
			}
		}
	}

	w.WriteHeader(statusCode)

	if _, err := w.Write(resp.Data); err != nil {
		log.Printf("gateway: ошибка записи ответа [%s]: %v", subject, err)
	}
}

// parseStatus разбирает строку HTTP-статуса ("200", "404" и т.д.) в int.
// Возвращает 0 при невалидном значении — caller подставляет дефолт.
func parseStatus(s string) int {
	var n int
	if _, err := fmt.Sscan(s, &n); err != nil || n < 100 || n > 599 {
		return 0
	}
	return n
}

// =============================================================================
// WebSocket
// =============================================================================

// handleWS апгрейдит HTTP-соединение до WebSocket и связывает его с NATS Pub/Sub.
//
// Схема работы:
//   - Gateway → Микросервис: connect-сообщение с SID и куками клиента
//   - Браузер → Gateway:     фреймы читаются и публикуются в {base}.in.{sid}
//   - Микросервис → Браузер: сообщения из {base}.out.{sid} пишутся в WS
//   - Закрытие сессии:       микросервис шлёт Header "Control: CLOSE"
func (gw *Gateway) handleWS(w http.ResponseWriter, r *http.Request, service string) {
	conn, err := gw.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// upgrader сам пишет HTTP-ошибку; логируем только для диагностики.
		log.Printf("gateway: WS upgrade error [%s]: %v", service, err)
		return
	}
	defer conn.Close()

	// Генерируем уникальный ID сессии (8 байт = 16 hex-символов).
	sidRaw := make([]byte, 8)
	if _, err := rand.Read(sidRaw); err != nil {
		log.Printf("gateway: ошибка генерации session ID: %v", err)
		return
	}
	sessionID := fmt.Sprintf("%x", sidRaw)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// mu защищает все записи в conn от гонки между горутиной Ping и NATS-коллбэком.
	var mu sync.Mutex

	safeWrite := func(msgType int, data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline))
		return conn.WriteMessage(msgType, data)
	}

	// PongHandler сдвигает ReadDeadline при каждом успешном Pong от клиента.
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(wsReadDeadline))
		return nil
	})

	baseSubject := fmt.Sprintf("api.v1.%s.ws", service)

	// Уведомляем микросервис об открытии новой WS-сессии.
	// Куки из HTTP-запроса апгрейда передаются в заголовке — микросервис
	// может прочитать access_token для аутентификации соединения.
	connectMsg := nats.NewMsg(baseSubject + ".connect")
	connectMsg.Header.Set("Sid", sessionID)
	if cookie := r.Header.Get("Cookie"); cookie != "" {
		connectMsg.Header.Set("Cookie", cookie)
	}
	if err := gw.nats.Conn.PublishMsg(connectMsg); err != nil {
		log.Printf("gateway: WS connect publish error [sid:%s]: %v", sessionID, err)
		return
	}

	// Подписка на исходящий поток: Микросервис → Браузер.
	sub, err := gw.nats.Conn.Subscribe(fmt.Sprintf("%s.out.%s", baseSubject, sessionID), func(m *nats.Msg) {
		if m.Header.Get("Control") == "CLOSE" {
			cancel()
			return
		}
		if err := safeWrite(websocket.TextMessage, m.Data); err != nil {
			log.Printf("gateway: WS write error [sid:%s]: %v", sessionID, err)
			cancel()
		}
	})
	if err != nil {
		log.Printf("gateway: WS subscribe error [%s, sid:%s]: %v", service, sessionID, err)
		return
	}
	defer sub.Unsubscribe()

	// Горутина Ping: держит соединение живым и выявляет зависших клиентов.
	go func() {
		ticker := time.NewTicker(wsPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := safeWrite(websocket.PingMessage, nil); err != nil {
					log.Printf("gateway: WS ping error [sid:%s]: %v", sessionID, err)
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	log.Printf("gateway: WS connected service=%s sid=%s remote=%s", service, sessionID, r.RemoteAddr)
	defer log.Printf("gateway: WS disconnected service=%s sid=%s", service, sessionID)

	// Основной цикл чтения: Браузер → Микросервис.
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(wsReadDeadline))

		_, data, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("gateway: WS read error [sid:%s]: %v", sessionID, err)
			}
			return
		}

		if err := gw.nats.Conn.Publish(fmt.Sprintf("%s.in.%s", baseSubject, sessionID), data); err != nil {
			log.Printf("gateway: NATS publish error [sid:%s]: %v", sessionID, err)
		}
	}
}
