// internal/middleware/xauth.go
//
// Демо-middleware для JWT-аутентификации.
// Использует xauth.VerifyJWT и xauth.Claims из демо-сервиса xauth.
//
// Пример применения — опциональная защита конкретного эндпоинта:
//
//	// Защищённый эндпоинт:
//	nc.QueueSubscribe("api.v1.{service}.create", queue,
//	    middleware.RequireAuth(authCfg, svc.HandleCreate),
//	)
//
//	// Публичный эндпоинт — без обёртки:
//	nc.QueueSubscribe("api.v1.{service}.list", queue, svc.HandleList)
package middleware

import (
	"encoding/json"
	"time"

	"platform/internal/services/xauth"
	"platform/utils"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// AuthConfig — параметры проверки JWT для middleware.
// Передаётся при регистрации подписки и хранится в замыкании — глобального состояния нет.
type AuthConfig struct {
	// AccessSecret — HMAC-секрет для проверки подписи access-токена.
	// Должен совпадать с секретом, которым сервис аутентификации подписывает access-токены.
	AccessSecret []byte

	// Log — логгер для событий авторизации.
	Log zerolog.Logger
}

// accessTokenClockSkew — допуск на расхождение часов между подписантом access-токена
// и проверяющим сервисом. Без него токен, выписанный «прямо сейчас» на узле A,
// может оказаться «истёкшим» на узле B при дрейфе NTP на пару секунд.
// 60 секунд — стандартный для индустрии запас (RFC 7519 §4.1.4 рекомендует
// «small leeway»); жёстче делать смысла нет — access-токены и так короткоживущие.
const accessTokenClockSkew = 60 * time.Second

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
		token := utils.GetCookie(msg, "access_token")
		if token == "" {
			replyUnauthorized(msg, cfg.Log, "access token missing")
			return
		}

		c, err := xauth.VerifyJWT(token, cfg.AccessSecret)
		if err != nil {
			cfg.Log.Warn().Err(err).Str("subject", msg.Subject).Msg("RequireAuth: невалидный токен")
			replyUnauthorized(msg, cfg.Log, "invalid access token")
			return
		}

		if time.Now().Unix() > c.Exp+int64(accessTokenClockSkew.Seconds()) {
			replyUnauthorized(msg, cfg.Log, "access token expired")
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
func replyUnauthorized(msg *nats.Msg, log zerolog.Logger, text string) {
	out := nats.NewMsg(msg.Reply)
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("Status", "401")
	// json.Marshal безошибочен для struct{string} — игнорируем error.
	out.Data, _ = json.Marshal(struct {
		Error string `json:"error"`
	}{Error: text})

	// Сам факт 401 не логируется (нормальный пользовательский кейс — токен протух,
	// клиент пойдёт на /refresh). Логируется только сбой доставки: пустой Reply,
	// разорванное соединение и пр. — это означает, что клиент не получил 401
	// и запрос «утёк» молча.
	if err := msg.RespondMsg(out); err != nil {
		log.Error().Err(err).Str("subject", msg.Subject).Msg("replyUnauthorized: ошибка отправки 401")
	}
}
