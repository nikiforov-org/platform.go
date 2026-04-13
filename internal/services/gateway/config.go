// internal/services/gateway/config.go
package gateway

import (
	"fmt"
	"os"
	"time"

	"platform/internal/platform/natsclient"
	"platform/utils"
)

// Config — полная конфигурация шлюза, собранная из переменных окружения.
// Является единственным источником конфигурации для cmd/gateway/main.go:
// никаких os.Getenv за пределами LoadConfig.
type Config struct {
	// HTTP — параметры входящего HTTP-сервера.
	HTTP HTTPConfig

	// NATS — параметры подключения к шине.
	NATS natsclient.Config

	// AllowedHosts — множество разрешённых Origin-хостов.
	// Пустое множество отключает проверку (dev-режим).
	AllowedHosts utils.AllowedHostSet
}

// HTTPConfig — параметры HTTP-сервера шлюза.
type HTTPConfig struct {
	// Addr — адрес и порт для входящих запросов (HTTP_ADDR).
	Addr string

	// ReadHeaderTimeout — защита от Slowloris-атак (HTTP_READ_HEADER_TIMEOUT).
	ReadHeaderTimeout time.Duration

	// ReadTimeout — таймаут на чтение всего запроса (HTTP_READ_TIMEOUT).
	ReadTimeout time.Duration

	// WriteTimeout — таймаут на запись ответа (HTTP_WRITE_TIMEOUT).
	// Для WebSocket-соединений апгрейдер сбрасывает его до нуля автоматически.
	WriteTimeout time.Duration

	// IdleTimeout — таймаут keep-alive соединений (HTTP_IDLE_TIMEOUT).
	IdleTimeout time.Duration
}

// LoadConfig читает все параметры конфигурации из переменных окружения.
//
// Переменные окружения и их значения по умолчанию:
//
//	HTTP_ADDR                  — адрес сервера                           (":8080")
//	HTTP_READ_HEADER_TIMEOUT   — таймаут чтения заголовков (формат: 5s)  ("5s")
//	HTTP_READ_TIMEOUT          — таймаут чтения запроса    (формат: 15s) ("15s")
//	HTTP_WRITE_TIMEOUT         — таймаут записи ответа     (формат: 15s) ("15s")
//	HTTP_IDLE_TIMEOUT          — таймаут keep-alive        (формат: 60s) ("60s")
//
//	NATS_HOST                  — хост NATS-сервера                       ("127.0.0.1")
//	NATS_PORT                  — клиентский порт NATS                    (4222)
//	NATS_USER                  — логин авторизации                       ("")
//	NATS_PASSWORD              — пароль авторизации                       ("")
//	NATS_RECONNECT_ATTEMPTS    — число попыток реконнекта (-1 = ∞)       (-1)
//	NATS_RECONNECT_WAIT        — пауза между попытками    (формат: 2s)   ("2s")
//
//	ALLOWED_HOSTS              — разрешённые Origin-хосты через ","      ("")
//
// Таймауты передаются в формате time.Duration: "5s", "1m30s" и т.д.
func LoadConfig() (Config, error) {
	// ALLOWED_HOSTS читается через os.Getenv, а не utils.GetEnv: значение содержит
	// запятые — fmt.Sscan остановился бы на первом разделителе.
	// Парсинг делегируется utils.ParseAllowedHosts.
	allowedHosts, err := utils.ParseAllowedHosts(os.Getenv("ALLOWED_HOSTS"))
	if err != nil {
		return Config{}, fmt.Errorf("ALLOWED_HOSTS: %w", err)
	}

	natsCfg := natsclient.DefaultConfig()
	natsCfg.Server.Host = utils.GetEnv("NATS_HOST", natsCfg.Server.Host)
	natsCfg.Server.ClientPort = utils.GetEnv("NATS_PORT", natsCfg.Server.ClientPort)
	natsCfg.Auth.User = utils.GetEnv("NATS_USER", "")
	natsCfg.Auth.Password = utils.GetEnv("NATS_PASSWORD", "")
	natsCfg.Reconnect.MaxAttempts = utils.GetEnv("NATS_RECONNECT_ATTEMPTS", natsCfg.Reconnect.MaxAttempts)
	natsCfg.Reconnect.WaitDuration = utils.GetEnv("NATS_RECONNECT_WAIT", 2*time.Second)

	return Config{
		HTTP: HTTPConfig{
			Addr:              utils.GetEnv("HTTP_ADDR", ":8080"),
			ReadHeaderTimeout: utils.GetEnv("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:       utils.GetEnv("HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:      utils.GetEnv("HTTP_WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:       utils.GetEnv("HTTP_IDLE_TIMEOUT", 60*time.Second),
		},
		NATS:         natsCfg,
		AllowedHosts: allowedHosts,
	}, nil
}
