// utils/reply.go
package utils

import (
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"
)

// NATSResponse — унифицированный конверт ответа любого микросервиса.
// Gateway читает заголовок "Status" для HTTP-кода, а тело передаёт клиенту как есть.
type NATSResponse struct {
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// Reply публикует успешный JSON-ответ в reply-subject входящего NATS-сообщения.
//
// status задаёт HTTP-код (200, 201, 204 и т.д.), который gateway выставит клиенту
// через заголовок "Status" — без десериализации тела ответа.
//
// extraHeaders — опциональные дополнительные заголовки в формате пар "ключ", "значение".
// Используется для передачи Set-Cookie из сервиса аутентификации через gateway в браузер:
//
//	utils.Reply(msg, 200, data, "Set-Cookie", accessCookie, "Set-Cookie", refreshCookie)
func Reply(msg *nats.Msg, status int, data any, extraHeaders ...string) {
	body, err := json.Marshal(NATSResponse{Data: data})
	if err != nil {
		pkgLog.Error().Err(err).Str("subject", msg.Subject).Msg("Reply: marshal error")
		natsRespond(msg, 500, []byte(`{"error":"internal error"}`))
		return
	}
	natsRespond(msg, status, body, extraHeaders...)
}

// ReplyError публикует ответ с кодом ошибки и текстом в поле "error".
func ReplyError(msg *nats.Msg, status int, errText string) {
	pkgLog.Error().Str("subject", msg.Subject).Int("status", status).Str("error", errText).Msg("ReplyError")
	// NATSResponse{Error: string} — маршалинг строки не падает; fallback для надёжности.
	body, err := json.Marshal(NATSResponse{Error: errText})
	if err != nil {
		body = []byte(`{"error":"internal error"}`)
	}
	natsRespond(msg, status, body)
}

// natsRespond — низкоуровневая публикация ответа через reply-subject.
// Использует msg.RespondMsg — не требует доступа к *nats.Conn.
func natsRespond(msg *nats.Msg, status int, body []byte, extraHeaders ...string) {
	out := nats.NewMsg(msg.Reply)
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("Status", fmt.Sprintf("%d", status))

	// Дополнительные заголовки передаются парами "ключ", "значение".
	// Add используется намеренно — Set-Cookie должен повторяться для каждой куки.
	for i := 0; i+1 < len(extraHeaders); i += 2 {
		out.Header.Add(extraHeaders[i], extraHeaders[i+1])
	}

	out.Data = body

	if err := msg.RespondMsg(out); err != nil {
		pkgLog.Error().Err(err).Str("subject", msg.Subject).Msg("natsRespond: ошибка отправки ответа")
	}
}
