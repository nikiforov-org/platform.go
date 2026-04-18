// internal/services/xauth/jwt.go
package xauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// jwtHeader — неизменяемая base64url-закодированная шапка всех токенов (HS256).
// Вычислена однократно: base64url({"alg":"HS256","typ":"JWT"})
const jwtHeader = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"

// Claims — полезная нагрузка JWT-токена (подмножество RFC 7519).
type Claims struct {
	Sub string `json:"sub"` // субъект (имя пользователя)
	Exp int64  `json:"exp"` // unix-время истечения
	Jti string `json:"jti"` // уникальный ID токена (используется для отзыва refresh)
	Iat int64  `json:"iat"` // unix-время выдачи
}

// SignJWT формирует подписанный JWT-токен (HS256) без внешних зависимостей.
// Формат: base64url(header).base64url(payload).base64url(HMAC-SHA256(header.payload))
func SignJWT(c Claims, secret []byte) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("jwt: marshal claims: %w", err)
	}

	hp := jwtHeader + "." + base64.RawURLEncoding.EncodeToString(payload)

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(hp))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return hp + "." + sig, nil
}

// VerifyJWT проверяет HMAC-SHA256 подпись токена и возвращает Claims.
//
// Срок действия (Exp) намеренно не проверяется здесь — это делает вызывающий код.
// Такое разделение позволяет вернуть клиенту разные сообщения:
// "токен истёк" (→ попробуй /refresh) vs "токен невалиден" (→ войди заново).
func VerifyJWT(token string, secret []byte) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("jwt: неверный формат токена")
	}

	// Defense-in-depth: токен подписан именно нашим SignJWT (один фиксированный
	// header HS256). Существующий HMAC-check уже отбрасывает подделки без
	// секрета, но явная проверка fail-fast и страхует от будущих правок,
	// которые могли бы начать диспатчить по alg из header.
	if parts[0] != jwtHeader {
		return Claims{}, fmt.Errorf("jwt: неподдерживаемый header")
	}

	hp := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(hp))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	// hmac.Equal выполняет сравнение за константное время — защита от timing-атак.
	if !hmac.Equal([]byte(parts[2]), []byte(expected)) {
		return Claims{}, fmt.Errorf("jwt: невалидная подпись")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("jwt: decode payload: %w", err)
	}

	var c Claims
	if err := json.Unmarshal(payloadBytes, &c); err != nil {
		return Claims{}, fmt.Errorf("jwt: unmarshal claims: %w", err)
	}

	return c, nil
}

// NewJTI генерирует криптографически случайный уникальный ID токена (JWT ID).
// Используется для идентификации refresh-токена в KV-хранилище при отзыве.
func NewJTI() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("jti: rand.Read: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
