// internal/middleware/recover.go
package middleware

import (
	"runtime/debug"

	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// Recover возвращает NATS-обёртку над next, которая перехватывает панику
// внутри обработчика, логирует её со стектрейсом и отвечает клиенту 500.
//
// NATS Go SDK не восстанавливается после паники в callback — без этой обёртки
// любая паника в обработчике роняет весь сервис. Recover изолирует сбой
// в рамках одного сообщения: сервис продолжает работу, клиент получает ответ.
//
// Применяется как внешний слой — оборачивает всю цепочку middleware:
//
//	nc.QueueSubscribe(subject, queue,
//	    middleware.Recover(log, middleware.RequireAuth(cfg, handler)),
//	)
func Recover(log zerolog.Logger, next nats.MsgHandler) nats.MsgHandler {
	return func(msg *nats.Msg) {
		defer func() {
			if r := recover(); r != nil {
				event := log.Error().
					Str("subject", msg.Subject).
					Interface("panic", r).
					Bytes("stack", debug.Stack())
				if reqID := msg.Header.Get("X-Request-Id"); reqID != "" {
					event = event.Str("req", reqID)
				}
				event.Msg("паника в обработчике")

				if msg.Reply == "" {
					return
				}

				out := nats.NewMsg(msg.Reply)
				out.Header.Set("Content-Type", "application/json")
				out.Header.Set("Status", "500")
				out.Data = []byte(`{"error":"internal server error"}`)

				if err := msg.RespondMsg(out); err != nil {
					log.Error().Err(err).Msg("Recover: ошибка ответа после паники")
				}
			}
		}()

		next(msg)
	}
}
