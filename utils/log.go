// utils/log.go
package utils

import "github.com/rs/zerolog"

// pkgLog — пакетный логгер утилит.
// По умолчанию nop: вывод до вызова SetLogger отбрасывается без паники.
var pkgLog = zerolog.Nop()

// SetLogger устанавливает логгер для пакета utils.
// Вызывается один раз при инициализации сервиса до первого использования utils.
func SetLogger(l zerolog.Logger) {
	pkgLog = l
}

// Logger возвращает текущий пакетный логгер.
// Используется пакетами, которые не хранят собственный экземпляр логгера.
func Logger() zerolog.Logger {
	return pkgLog
}
