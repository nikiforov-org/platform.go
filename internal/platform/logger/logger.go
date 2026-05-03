// internal/platform/logger/logger.go
//
// logger — инициализация структурированного логгера для микросервисов платформы.
// Каждый сервис создаёт свой экземпляр через New, получая logger с полем "service".
//
// Уровень логирования задаётся через переменную окружения PLATFORM_LOG_LEVEL:
//
//	PLATFORM_LOG_LEVEL=debug   — все сообщения, включая отладочные
//	PLATFORM_LOG_LEVEL=info    — информационные, предупреждения, ошибки (по умолчанию)
//	PLATFORM_LOG_LEVEL=warn    — только предупреждения и ошибки
//	PLATFORM_LOG_LEVEL=error   — только ошибки
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
	if raw := strings.ToLower(os.Getenv("PLATFORM_LOG_LEVEL")); raw != "" {
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
