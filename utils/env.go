// utils/env.go
package utils

import (
	"fmt"
	"log"
	"os"
)

// GetEnv читает переменную окружения key и парсит её в тип T.
//
// Если переменная не задана или её значение нельзя привести к типу T,
// возвращается fallback. Некорректное (непустое) значение логируется как
// предупреждение — молчаливый откат к дефолту опасен в production.
//
// Поддерживаемые типы T: все, которые умеет парсить fmt.Sscan —
// string, int, int64, float64, bool, time.Duration и их производные.
func GetEnv[T any](key string, fallback T) T {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}

	var result T
	if _, err := fmt.Sscan(v, &result); err != nil {
		log.Printf("WARN: GetEnv: не удалось распарсить %s=%q: %v; используется значение по умолчанию", key, v, err)
		return fallback
	}

	return result
}
