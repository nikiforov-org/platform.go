// internal/middleware/xauth.go
//
// Пакет middleware содержит обёртки над nats.MsgHandler для сквозной логики.
//
// В отличие от HTTP-middleware, NATS-middleware — это функция высшего порядка:
// принимает следующий обработчик и возвращает новый с расширенным поведением.
//
// Пример применения — опциональная защита конкретного эндпоинта:
//
//	// Защищённый эндпоинт:
//	nc.QueueSubscribe("api.v1.xhttp.create", queue,
//	    middleware.RequireAuth(authCfg, svc.HandleCreate),
//	)
//
//	// Публичный эндпоинт — без обёртки:
//	nc.QueueSubscribe("api.v1.xhttp.list", queue, svc.HandleList)
package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// AuthConfig — параметры проверки JWT для middleware.
// Передаётся при регистрации подписки и хранится в замыкании — глобального состояния нет.
type AuthConfig struct {
	// AccessSecret — HMAC-секрет для проверки подписи access-токена.
	// Должен совпадать с AUTH_ACCESS_SECRET сервиса xauth.
	AccessSecret []byte

	// Log — логгер для событий авторизации.
	Log zerolog.Logger
}

// Claims — полезная нагрузка JWT access-токена.
type Claims struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
	Jti string `json:"jti"`
	Iat int64  `json:"iat"`
}

// RequireAuth возвращает NATS-обёртку над next, которая:
//
//  1. Извлекает access_token из куки (заголовок "Cookie" в NATS-сообщении).
//  2. Проверяет HMAC-подпись и срок действия токена.
//  3. При успехе добавляет заголовок "X-Auth-Sub" с именем субъекта
//     и передаёт управление в next.
//  4. При любой ошибке авторизации отвечает 401 и не вызывает next.
//
// Middleware не делает сетевых запросов — проверка полностью локальная (HMAC + Exp).
// Access-токен живёт недолго (обычно 15 минут), поэтому отзыв через KV не нужен.
// Для принудительного отзыва используйте короткий AUTH_ACCESS_TTL.
func RequireAuth(cfg AuthConfig, next nats.MsgHandler) nats.MsgHandler {
	return func(msg *nats.Msg) {
		token := cookieFromMsg(msg, "access_token")
		if token == "" {
			replyUnauthorized(msg, "access token missing")
			return
		}

		c, err := verifyJWT(token, cfg.AccessSecret)
		if err != nil {
			cfg.Log.Warn().Err(err).Str("subject", msg.Subject).Msg("RequireAuth: невалидный токен")
			replyUnauthorized(msg, "invalid access token")
			return
		}

		if time.Now().Unix() > c.Exp {
			replyUnauthorized(msg, "access token expired")
			return
		}

		// Передаём субъект следующему обработчику через заголовок сообщения.
		// Хендлер читает его как: subject := msg.Header.Get("X-Auth-Sub")
		msg.Header.Set("X-Auth-Sub", c.Sub)

		next(msg)
	}
}

// =============================================================================
// Вспомогательные функции (пакет-приватные)
// =============================================================================

// replyUnauthorized отвечает 401 через reply-subject сообщения.
// Использует msg.RespondMsg — не требует доступа к *nats.Conn.
func replyUnauthorized(msg *nats.Msg, text string) {
	out := nats.NewMsg(msg.Reply)
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("Status", "401")
	out.Data = []byte(`{"error":"` + text + `"}`)

	if err := msg.RespondMsg(out); err != nil {
		// Логировать некуда — функция не имеет доступа к логгеру.
		// Ошибка здесь означает потерю reply-subject, что крайне редко.
		_ = err
	}
}

// cookieFromMsg извлекает значение куки из заголовка "Cookie" NATS-сообщения.
// Переиспользует стандартный парсер http.Request — без самописного парсинга.
func cookieFromMsg(msg *nats.Msg, name string) string {
	raw := msg.Header.Get("Cookie")
	if raw == "" {
		return ""
	}
	r := &http.Request{Header: http.Header{"Cookie": []string{raw}}}
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// verifyJWT проверяет HMAC-SHA256 подпись JWT-токена и возвращает Claims.
// Срок действия (Exp) не проверяется здесь — это ответственность вызывающего кода,
// чтобы различить "токен истёк" и "токен невалиден" и вернуть разные сообщения.
func verifyJWT(token string, secret []byte) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("неверный формат токена")
	}

	hp := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(hp))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	// hmac.Equal выполняет сравнение за константное время — защита от timing-атак.
	if !hmac.Equal([]byte(parts[2]), []byte(expected)) {
		return Claims{}, fmt.Errorf("невалидная подпись")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("decode payload: %w", err)
	}

	var c Claims
	if err := json.Unmarshal(payloadBytes, &c); err != nil {
		return Claims{}, fmt.Errorf("unmarshal claims: %w", err)
	}

	return c, nil
}
