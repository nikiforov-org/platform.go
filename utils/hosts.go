// utils/hosts.go
package utils

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// AllowedHostSet — множество разрешённых Origin-хостов (host или host:port).
//
// Используется gateway для проверки заголовка Origin во входящих HTTP
// и WebSocket запросах. Пустое множество отключает проверку (dev-режим).
type AllowedHostSet map[string]struct{}

// ParseAllowedHosts разбирает строку вида "localhost:3000,platform.go" в AllowedHostSet.
//
// Каждый элемент может быть:
//   - голым хостом:            "platform.go"
//   - хостом с портом:         "localhost:3000"
//   - полным Origin со схемой: "http://localhost:3000" — схема отбрасывается,
//     в множество попадает только host-часть, т.к. браузерный Origin включает схему,
//     а в .env удобнее писать без неё.
//
// Пустая строка возвращает пустое множество (проверка Origin отключена).
func ParseAllowedHosts(raw string) (AllowedHostSet, error) {
	if raw == "" {
		return AllowedHostSet{}, nil
	}

	set := make(AllowedHostSet)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		host, err := extractHost(entry)
		if err != nil {
			return nil, fmt.Errorf("невалидный хост %q: %w", entry, err)
		}
		set[host] = struct{}{}
	}

	if len(set) > 0 {
		hosts := make([]string, 0, len(set))
		for h := range set {
			hosts = append(hosts, h)
		}
		pkgLog.Info().Strs("hosts", hosts).Msg("разрешённые хосты Origin")
	}

	return set, nil
}

// MustParseAllowedHosts — вариант ParseAllowedHosts для случаев,
// когда невалидный ALLOWED_HOSTS является фатальной ошибкой конфигурации.
func MustParseAllowedHosts(raw string) AllowedHostSet {
	set, err := ParseAllowedHosts(raw)
	if err != nil {
		pkgLog.Fatal().Err(err).Msg("ALLOWED_HOSTS: невалидный хост")
	}
	return set
}

// Allows проверяет, разрешён ли данный Origin.
//
// Правила (в порядке приоритета):
//  1. Пустое множество (ALLOWED_HOSTS не задан) — разрешаем всё.
//  2. Нет заголовка Origin (curl, серверный вызов, health check) — разрешаем:
//     Origin шлют только браузеры при кросс-доменных запросах.
//  3. Иначе — извлекаем host из Origin и ищем его в множестве.
func (s AllowedHostSet) Allows(origin string) bool {
	if len(s) == 0 {
		return true
	}
	if origin == "" {
		return true
	}

	host, err := extractHost(origin)
	if err != nil {
		pkgLog.Warn().Str("origin", origin).Err(err).Msg("невалидный Origin")
		return false
	}

	_, ok := s[host]
	return ok
}

// extractHost возвращает host (или host:port) из произвольной строки.
// Если строка содержит схему ("http://...") — парсится как URL.
// Иначе строка принимается как есть.
func extractHost(s string) (string, error) {
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", err
		}
		if u.Host == "" {
			return "", fmt.Errorf("не удалось извлечь host из %q", s)
		}
		return u.Host, nil
	}
	return s, nil
}

// AllowedHostsFromEnv читает и парсит ALLOWED_HOSTS из окружения.
// Удобный shortcut для использования в LoadConfig сервисов.
func AllowedHostsFromEnv() (AllowedHostSet, error) {
	return ParseAllowedHosts(os.Getenv("ALLOWED_HOSTS"))
}
