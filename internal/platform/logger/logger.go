// internal/platform/logger/logger.go
//
// logger — инициализация структурированного логгера для микросервисов платформы.
// Каждый сервис создаёт свой экземпляр через New, получая logger с полем "service".
//
// Уровень логирования задаётся через переменную окружения LOG_LEVEL:
//
//	LOG_LEVEL=debug   — все сообщения, включая отладочные
//	LOG_LEVEL=info    — информационные, предупреждения, ошибки (по умолчанию)
//	LOG_LEVEL=warn    — только предупреждения и ошибки
//	LOG_LEVEL=error   — только ошибки
package logger

import (
	"os"
	"strings"

	"github.com/rs/zerolog"
)

// New создаёт zerolog.Logger для указанного сервиса.
// Все сообщения пишутся в stderr в формате JSON.
// Каждое сообщение содержит поля: time, level, service, message.
//
// Уровень выставляется на конкретный экземпляр через .Level(...), а не через
// zerolog.SetGlobalLevel: глобальный side-effect ломал бы параллельные тесты
// и любой сценарий с несколькими логгерами в одном процессе.
func New(service string) zerolog.Logger {
	level := zerolog.InfoLevel
	if raw := strings.ToLower(os.Getenv("LOG_LEVEL")); raw != "" {
		if lvl, err := zerolog.ParseLevel(raw); err == nil {
			level = lvl
		}
	}

	return zerolog.New(os.Stderr).
		Level(level).
		With().
		Timestamp().
		Str("service", service).
		Logger()
}
