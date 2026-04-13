// utils/reply.go
package utils

import (
	"encoding/json"
	"fmt"
	"log"

	natsgo "github.com/nats-io/nats.go"
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
// Используется для передачи Set-Cookie из auth-ms через gateway в браузер:
//
//	utils.Reply(msg, 200, data, "Set-Cookie", accessCookie, "Set-Cookie", refreshCookie)
func Reply(msg *natsgo.Msg, status int, data any, extraHeaders ...string) {
	body, _ := json.Marshal(NATSResponse{Data: data})
	natsRespond(msg, status, body, extraHeaders...)
}

// ReplyError публикует ответ с кодом ошибки и текстом в поле "error".
func ReplyError(msg *natsgo.Msg, status int, errText string) {
	log.Printf("utils: [%s] %d %s", msg.Subject, status, errText)
	body, _ := json.Marshal(NATSResponse{Error: errText})
	natsRespond(msg, status, body)
}

// natsRespond — низкоуровневая публикация ответа через reply-subject.
// Использует msg.RespondMsg — не требует доступа к *natsgo.Conn.
func natsRespond(msg *natsgo.Msg, status int, body []byte, extraHeaders ...string) {
	out := natsgo.NewMsg(msg.Reply)
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("Status", fmt.Sprintf("%d", status))

	// Дополнительные заголовки передаются парами "ключ", "значение".
	// Add используется намеренно — Set-Cookie должен повторяться для каждой куки.
	for i := 0; i+1 < len(extraHeaders); i += 2 {
		out.Header.Add(extraHeaders[i], extraHeaders[i+1])
	}

	out.Data = body

	if err := msg.RespondMsg(out); err != nil {
		log.Printf("utils: natsRespond: %v", err)
	}
}
