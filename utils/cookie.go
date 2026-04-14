// utils/cookie.go
package utils

import (
	"net/http"

	nats "github.com/nats-io/nats.go"
)

// GetCookie извлекает значение куки из заголовка "Cookie" NATS-сообщения.
//
// Gateway пробрасывает HTTP-заголовок Cookie клиента в NATS как есть,
// поэтому для парсинга переиспользуется стандартный http.Request.Cookie —
// без самописного парсера, который неизбежно сломается на edge-cases
// (кавычки, пробелы, спецсимволы).
func GetCookie(msg *nats.Msg, name string) string {
	raw := msg.Header.Get("Cookie")
	if raw == "" {
		return ""
	}

	r := &http.Request{Header: http.Header{"Cookie": []string{raw}}}
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}

	return c.Value
}

// BuildSetCookie формирует строку значения заголовка Set-Cookie.
//
// Используется вместо прямой записи в http.ResponseWriter, потому что
// микросервис общается с gateway через NATS-заголовки, а не напрямую с браузером.
// http.Cookie.String() корректно сериализует все атрибуты (HttpOnly, SameSite и т.д.).
//
//   - name, value — имя и значение куки
//   - domain       — домен; пустая строка означает текущий домен запроса
//   - maxAge       — время жизни в секундах; -1 удаляет куку немедленно
//   - secure       — выставить флаг Secure (только HTTPS)
func BuildSetCookie(name, value, domain string, maxAge int, secure bool) string {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Domain:   domain,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
	return c.String()
}
