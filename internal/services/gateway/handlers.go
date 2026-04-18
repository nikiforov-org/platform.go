// internal/services/gateway/handlers.go
package gateway

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
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

// wsReadLimit — максимальный размер одного входящего WS-сообщения.
// Без него gorilla читает фреймы любого размера → memory-DoS.
const wsReadLimit = 64 * 1024

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
	// ctx отменяется при штатном завершении Gateway (закрытии stop-канала).
	// WS-обработчики наследуются от него — shutdown корректно прерывает все сессии.
	ctx context.Context
}

// New создаёт Gateway с переданными зависимостями.
// stop закрывается при штатном завершении — освобождает rate limiter и отменяет
// все активные WS-сессии через gw.ctx.
// CheckOrigin делегируется allowedHosts — HTTP и WebSocket используют одно правило.
func New(natsClient *nc.PlatformClient, cfg Config, log zerolog.Logger, stop <-chan struct{}) *Gateway {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stop
		cancel()
	}()

	gw := &Gateway{
		nats:         natsClient,
		allowedHosts: cfg.AllowedHosts,
		cfg:          cfg,
		rl:           newRateLimiter(cfg.RateLimit, log, stop),
		log:          log,
		ctx:          ctx,
	}
	gw.upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return cfg.AllowedHosts.Allows(log, r.Header.Get("Origin"))
		},
	}
	return gw
}

// RootContext возвращает корневой контекст шлюза.
// Отменяется при закрытии stop-канала (graceful shutdown).
// Предназначен для http.Server.BaseContext — чтобы все входящие запросы
// наследовали shutdown-сигнал и могли его наблюдать через r.Context().
func (gw *Gateway) RootContext() context.Context {
	return gw.ctx
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

	// Ошибки w.Write игнорируем намеренно: WriteHeader уже улетел, второй ответ
	// не послать; разрыв TCP клиентом для healthcheck — обычное дело.
	if gw.nats.Conn.IsConnected() {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","nats":"connected"}`))
		return
	}

	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"status":"error","nats":"disconnected"}`))
}

// =============================================================================
// Маршрутизация
// =============================================================================

// validSubjectToken — допустимые символы в одном сегменте URL после /v1/.
// Эти сегменты идут в NATS subject; `*`/`>` — wildcards, пробел/control bytes —
// невалидные subject-токены. Whitelist предотвращает инъекцию в маршрутизацию.
var validSubjectToken = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

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
		if origin != "" && !gw.allowedHosts.Allows(gw.log, origin) {
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

	// Все сегменты после /v1/ идут в NATS subject — валидируем единым правилом.
	// Покрывает и service, и methodParts; в WS-ветке последний токен "ws" проходит regex.
	for _, p := range parts[1:] {
		if !validSubjectToken.MatchString(p) {
			http.Error(w, "invalid path segment", http.StatusBadRequest)
			return
		}
	}
	service := parts[1]

	if parts[len(parts)-1] == "ws" {
		// RFC 6455 §4.1: WebSocket handshake требует GET.
		// Отбиваем до wsConnGuard/Upgrade, чтобы невалидный метод не резервировал слот.
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
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
	reqID := newRequestID()
	method := strings.Join(methodParts, ".")
	subject := fmt.Sprintf("api.v1.%s.%s", service, method)
	ip := realIP(r, gw.cfg.RateLimit.TrustedProxy)
	start := time.Now()

	gw.log.Info().
		Str("req", reqID).
		Str("method", r.Method).
		Str("path", r.URL.Path).
		Str("ip", ip).
		Msg("→")

	// Ограничиваем чтение тела 1 МБ для защиты от злоупотреблений.
	const maxBodySize = 1 << 20 // 1 MB
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
	if err != nil {
		gw.log.Error().Err(err).Str("req", reqID).Str("subject", subject).Msg("ошибка чтения тела запроса")
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	msg := nats.NewMsg(subject)
	msg.Data = body
	msg.Header.Set("X-Real-IP", ip)
	msg.Header.Set("X-Request-Id", reqID)

	if auth := r.Header.Get("Authorization"); auth != "" {
		msg.Header.Set("Authorization", auth)
	}
	if cookie := r.Header.Get("Cookie"); cookie != "" {
		msg.Header.Set("Cookie", cookie)
	}

	// Контекст NATS-запроса: клиентская отмена (r.Context) + shutdown (BaseContext=gw.ctx)
	// + явный таймаут на случай зависания NATS/бекенда.
	ctx, cancel := context.WithTimeout(r.Context(), gw.cfg.HTTP.NATSRequestTimeout)
	defer cancel()

	resp, err := gw.nats.Conn.RequestMsgWithContext(ctx, msg)
	if err != nil {
		status, reason := natsRequestErrStatus(r.Context(), err)
		gw.log.Error().Err(err).Str("req", reqID).Str("subject", subject).Int("status", status).Str("reason", reason).Msg("NATS request error")
		http.Error(w, reason, status)
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

	// Возвращаем request ID клиенту — полезно для поддержки и отладки.
	w.Header().Set("X-Request-Id", reqID)
	w.WriteHeader(statusCode)

	if _, err := w.Write(resp.Data); err != nil {
		gw.log.Error().Err(err).Str("req", reqID).Str("subject", subject).Msg("ошибка записи ответа")
		return
	}

	gw.log.Info().
		Str("req", reqID).
		Str("subject", subject).
		Int("status", statusCode).
		Int64("ms", time.Since(start).Milliseconds()).
		Msg("←")
}

// isTimeoutError возвращает true если ошибка — превышение дедлайна на чтение.
func isTimeoutError(err error) bool {
	if ne, ok := err.(interface{ Timeout() bool }); ok {
		return ne.Timeout()
	}
	return false
}

// natsRequestErrStatus классифицирует ошибку NATS Request-Reply в HTTP-статус.
//
//   - 499 "client closed request" — клиент отменил запрос (закрыл соединение,
//     shutdown сервера). Нестандартный, но распространённый (nginx) код; ответ
//     обычно до клиента не доходит, код полезен в логах и метриках.
//   - 504 "gateway timeout"       — сработал собственный таймаут gateway
//     (GATEWAY_NATS_REQUEST_TIMEOUT), клиент ещё подключён.
//   - 503 "service unavailable"   — прочие ошибки (NATS disconnected, no responders).
func natsRequestErrStatus(clientCtx context.Context, err error) (int, string) {
	// Проверяем клиентский контекст отдельно: именно он различает "клиент отменил"
	// от "наш таймаут истёк". WithTimeout поверх r.Context() возвращает DeadlineExceeded
	// в обоих случаях — clientCtx.Err() даёт однозначный ответ.
	if clientCtx.Err() == context.Canceled {
		return 499, "client closed request"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "gateway timeout"
	}
	return http.StatusServiceUnavailable, "service unavailable"
}

// newRequestID генерирует уникальный идентификатор запроса (8 байт = 16 hex-символов).
// Используется для сквозной трассировки: Gateway → NATS-заголовок → лог сервиса.
func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return fmt.Sprintf("%x", b)
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

	// Генерируем sessionID (8 байт = 16 hex) ДО Upgrade: при сбое CSPRNG
	// отвечаем обычным HTTP 500. После Upgrade соединение уже WebSocket —
	// корректное закрытие требовало бы отдельного CloseMessage по RFC 6455.
	sidRaw := make([]byte, 8)
	if _, err := rand.Read(sidRaw); err != nil {
		releaseConn()
		gw.log.Error().Err(err).Str("service", service).Msg("ошибка генерации session ID")
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	sessionID := fmt.Sprintf("%x", sidRaw)

	conn, err := gw.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// upgrader сам пишет HTTP-ошибку; логируем только для диагностики.
		releaseConn()
		gw.log.Error().Err(err).Str("service", service).Msg("WS upgrade error")
		return
	}
	defer releaseConn()

	conn.SetReadLimit(wsReadLimit)

	// Наследуемся от gw.ctx: при shutdown сервера все WS-сессии отменяются.
	ctx, cancel := context.WithCancel(gw.ctx)

	// mu защищает ВСЕ обращения к conn (Write, Close, SetReadDeadline) от гонок между:
	//   - горутиной Ping,
	//   - NATS-коллбэком подписки .out.{sid},
	//   - shutdown-горутиной,
	//   - основным циклом ReadMessage.
	// Close() тоже захватывает mu — гарантирует, что WriteMessage не может выполняться
	// параллельно с закрытием. ctx.Done() под тем же mu — барьер от записи в уже
	// отменённую сессию.
	var mu sync.Mutex

	// wg ждёт завершения побочных горутин (Ping, shutdown) до conn.Close.
	// Без него горутина может оказаться в safeWrite/safeSetReadDeadline после Close —
	// формально race по go race detector, плюс ссылки на conn/ctx удерживаются дольше handleWS.
	var wg sync.WaitGroup

	safeWrite := func(msgType int, data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline))
		return conn.WriteMessage(msgType, data)
	}

	// safeSetReadDeadline — синхронизированная установка ReadDeadline под mu.
	// Используется shutdown-горутиной для прерывания блокирующего ReadMessage:
	// без mu вызов мог идти параллельно с conn.Close из основного defer.
	safeSetReadDeadline := func(t time.Time) error {
		mu.Lock()
		defer mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		return conn.SetReadDeadline(t)
	}

	// Объединённый cancel + wait + close. Порядок важен:
	//   1. cancel() — сигнал всем горутинам (Ping, shutdown, NATS callback),
	//      safeWrite/safeSetReadDeadline после этого вернутся без обращения к conn.
	//   2. wg.Wait() — ждём завершения Ping и shutdown горутин, чтобы они не оказались
	//      в полёте уже после conn.Close.
	//   3. mu.Lock() — ждём завершения текущего WriteMessage из NATS-коллбэка, если в полёте.
	//   4. conn.Close() — финальное закрытие WS-соединения.
	// defer срабатывает ПОСЛЕ defer sub.Unsubscribe() (LIFO): сначала отписка от NATS,
	// чтобы новые коллбэки не стартовали, потом cancel → wait → закрытие.
	defer func() {
		cancel()
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		_ = conn.Close()
	}()

	// PongHandler сдвигает ReadDeadline при каждом успешном Pong от клиента.
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(wsReadDeadline))
		return nil
	})

	baseSubject := fmt.Sprintf("api.v1.%s.ws", service)

	// Уведомляем микросервис об открытии новой WS-сессии и ждём ack.
	// Request-Reply вместо Publish: при отсутствии подписчика (сервис не запущен,
	// scale-to-zero) NATS возвращает ErrNoResponders мгновенно, при перегрузке —
	// DeadlineExceeded по WSConnectTimeout. В обоих случаях клиенту отправляется
	// WS Close 1011, чтобы он не висел с «зомби»-сессией до wsReadDeadline.
	// Куки из HTTP-запроса апгрейда передаются в заголовке — микросервис
	// может прочитать access_token для аутентификации соединения.
	connectMsg := nats.NewMsg(baseSubject + ".connect")
	connectMsg.Header.Set("Sid", sessionID)
	if cookie := r.Header.Get("Cookie"); cookie != "" {
		connectMsg.Header.Set("Cookie", cookie)
	}
	connectCtx, connectCancel := context.WithTimeout(ctx, gw.cfg.HTTP.WSConnectTimeout)
	_, err = gw.nats.Conn.RequestMsgWithContext(connectCtx, connectMsg)
	connectCancel()
	if err != nil {
		event := gw.log.Error()
		if errors.Is(err, nats.ErrNoResponders) {
			event = gw.log.Warn()
		}
		event.Err(err).Str("service", service).Str("sid", sessionID).Msg("WS connect ack error")
		_ = safeWrite(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "service unavailable"))
		return
	}

	// Подписка на исходящий поток: Микросервис → Браузер.
	sub, err := gw.nats.Conn.Subscribe(fmt.Sprintf("%s.out.%s", baseSubject, sessionID), func(m *nats.Msg) {
		// Быстрый выход без lock contention: при массовом shutdown очередь коллбэков
		// NATS-диспетчера не будет толпиться на mu — каждый сразу увидит ctx.Done.
		if ctx.Err() != nil {
			return
		}
		if m.Header.Get("Control") == "CLOSE" {
			cancel()
			return
		}
		if err := safeWrite(websocket.TextMessage, m.Data); err != nil {
			// context.Canceled — штатный выход при shutdown, не ошибка.
			if !errors.Is(err, context.Canceled) {
				gw.log.Error().Err(err).Str("sid", sessionID).Msg("WS write error")
			}
			cancel()
		}
	})
	if err != nil {
		gw.log.Error().Err(err).Str("service", service).Str("sid", sessionID).Msg("WS subscribe error")
		return
	}
	defer sub.Unsubscribe()

	// Горутина Ping: держит соединение живым и выявляет зависших клиентов.
	wg.Add(1)
	go func() {
		defer wg.Done()
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

	// Горутина shutdown: при завершении сервера отправляет клиенту close-фрейм
	// и прерывает блокирующий ReadMessage через SetReadDeadline(now).
	// Без этого ReadMessage ждал бы до wsReadDeadline (60s), блокируя server.Shutdown.
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-gw.ctx.Done():
			_ = safeWrite(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "server restarting"))
			_ = safeSetReadDeadline(time.Now())
		case <-ctx.Done():
			// Сессия завершилась раньше shutdown — горутина не нужна.
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
			switch {
			case websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway):
				// Клиент закрыл соединение штатно.
			case isTimeoutError(err):
				// Клиент не прислал данные или Pong за wsReadDeadline — считаем мёртвым.
				gw.log.Info().Str("sid", sessionID).Msg("WS session timed out")
				safeWrite(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "inactivity timeout"))
			default:
				gw.log.Error().Err(err).Str("sid", sessionID).Msg("WS read error")
			}
			return
		}

		if err := gw.nats.Conn.Publish(fmt.Sprintf("%s.in.%s", baseSubject, sessionID), data); err != nil {
			gw.log.Error().Err(err).Str("sid", sessionID).Msg("NATS publish error")
		}
	}
}
