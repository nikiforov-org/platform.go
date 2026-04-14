// internal/services/gateway/handlers.go
package gateway

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"platform/internal/platform/nc"
	"platform/utils"

	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
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
	nats         *nc.PlatformClient
	upgrader     websocket.Upgrader
	allowedHosts utils.AllowedHostSet
	cfg          Config
	rl           *rl
	log          zerolog.Logger
}

// New создаёт Gateway с переданными зависимостями.
// stop закрывается при штатном завершении — освобождает фоновую горутину rate limiter.
// CheckOrigin делегируется allowedHosts — HTTP и WebSocket используют одно правило.
func New(natsClient *nc.PlatformClient, cfg Config, log zerolog.Logger, stop <-chan struct{}) *Gateway {
	gw := &Gateway{
		nats:         natsClient,
		allowedHosts: cfg.AllowedHosts,
		cfg:          cfg,
		rl:           newRateLimiter(cfg.RateLimit, log, stop),
		log:          log,
	}
	gw.upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return cfg.AllowedHosts.Allows(r.Header.Get("Origin"))
		},
	}
	return gw
}

// Handler возвращает корневой http.Handler шлюза.
//
// Маршруты:
//   - /health — health check (вне rate limit и Origin, не проксируется в NATS)
//   - /v1/     — API: Origin → RateLimit → маршрутизация (HTTP RPC или WebSocket)
func (gw *Gateway) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("/v1/", gw.route)

	root := http.NewServeMux()
	root.HandleFunc("/health", gw.handleHealth)
	root.Handle("/", gw.middlewareOrigin(gw.middlewareRateLimit(api)))
	return root
}

// handleHealth отвечает на запросы проверки здоровья сервиса.
//
// Проверяет доступность NATS-соединения. Используется оркестратором (Nomad)
// для определения готовности Gateway к обработке трафика.
//
// Ответы:
//
//	200 {"status":"ok",  "nats":"connected"}    — Gateway готов
//	503 {"status":"error","nats":"disconnected"} — NATS недоступен
func (gw *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if gw.nats.Conn.IsConnected() {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","nats":"connected"}`))
		return
	}

	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write([]byte(`{"status":"error","nats":"disconnected"}`))
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
			gw.log.Warn().Str("origin", origin).Str("method", r.Method).Str("path", r.URL.Path).Msg("отклонён Origin")
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
		gw.log.Error().Err(err).Str("subject", subject).Msg("ошибка чтения тела запроса")
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
		gw.log.Error().Err(err).Str("subject", subject).Msg("NATS request error")
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
		gw.log.Error().Err(err).Str("subject", subject).Msg("ошибка записи ответа")
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
	// Проверяем глобальный лимит WS-соединений до апгрейда.
	ok, releaseConn := gw.wsConnGuard()
	if !ok {
		gw.log.Warn().Str("service", service).Int64("limit", gw.cfg.RateLimit.MaxWSConns).Msg("WS connections limit reached")
		http.Error(w, `{"error":"too many connections"}`, http.StatusServiceUnavailable)
		return
	}

	conn, err := gw.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// upgrader сам пишет HTTP-ошибку; логируем только для диагностики.
		releaseConn()
		gw.log.Error().Err(err).Str("service", service).Msg("WS upgrade error")
		return
	}
	defer releaseConn()
	defer conn.Close()

	// Генерируем уникальный ID сессии (8 байт = 16 hex-символов).
	sidRaw := make([]byte, 8)
	if _, err := rand.Read(sidRaw); err != nil {
		gw.log.Error().Err(err).Msg("ошибка генерации session ID")
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
		gw.log.Error().Err(err).Str("sid", sessionID).Msg("WS connect publish error")
		return
	}

	// Подписка на исходящий поток: Микросервис → Браузер.
	sub, err := gw.nats.Conn.Subscribe(fmt.Sprintf("%s.out.%s", baseSubject, sessionID), func(m *nats.Msg) {
		if m.Header.Get("Control") == "CLOSE" {
			cancel()
			return
		}
		if err := safeWrite(websocket.TextMessage, m.Data); err != nil {
			gw.log.Error().Err(err).Str("sid", sessionID).Msg("WS write error")
			cancel()
		}
	})
	if err != nil {
		gw.log.Error().Err(err).Str("service", service).Str("sid", sessionID).Msg("WS subscribe error")
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
					gw.log.Error().Err(err).Str("sid", sessionID).Msg("WS ping error")
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	gw.log.Info().Str("service", service).Str("sid", sessionID).Str("remote", r.RemoteAddr).Msg("WS connected")
	defer gw.log.Info().Str("service", service).Str("sid", sessionID).Msg("WS disconnected")

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
				gw.log.Error().Err(err).Str("sid", sessionID).Msg("WS read error")
			}
			return
		}

		if err := gw.nats.Conn.Publish(fmt.Sprintf("%s.in.%s", baseSubject, sessionID), data); err != nil {
			gw.log.Error().Err(err).Str("sid", sessionID).Msg("NATS publish error")
		}
	}
}
