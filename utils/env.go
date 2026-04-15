// utils/env.go
package utils

import (
	"fmt"
	"os"
	"time"
)

// GetEnv читает переменную окружения key и парсит её в тип T.
//
// Если переменная не задана или её значение нельзя привести к типу T,
// возвращается fallback. Некорректное (непустое) значение логируется как
// предупреждение — молчаливый откат к дефолту опасен в production.
//
// Поддерживаемые типы T: string, int, int64, float64, bool — через fmt.Sscan;
// time.Duration — через time.ParseDuration ("15m", "30s", "168h" и т.д.).
//
// Важно: fmt.Sscan не умеет парсить time.Duration с единицами измерения —
// "15m" он разбирает как целое 15 (= 15ns), а не 15 минут. Поэтому для
// time.Duration используется time.ParseDuration.
func GetEnv[T any](key string, fallback T) T {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}

	var result T

	// time.Duration требует специальной обработки: fmt.Sscan парсит только
	// цифровой префикс и игнорирует единицу ("15m" → 15, т.е. 15ns).
	if dp, ok := any(&result).(*time.Duration); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			pkgLog.Warn().Str("key", key).Str("value", v).Err(err).Msg("GetEnv: не удалось распарсить duration, используется значение по умолчанию")
			return fallback
		}
		*dp = d
		return result
	}

	if _, err := fmt.Sscan(v, &result); err != nil {
		pkgLog.Warn().Str("key", key).Str("value", v).Err(err).Msg("GetEnv: не удалось распарсить переменную, используется значение по умолчанию")
		return fallback
	}

	return result
}
