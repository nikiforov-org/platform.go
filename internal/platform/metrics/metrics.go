// Package metrics — Prometheus-метрики платформы.
//
// Экспонируются через HTTP-эндпоинт /metrics (см. Serve), скрейп выполняется
// внешним Prometheus. Bind по умолчанию — loopback (127.0.0.1), доступ снаружи
// предполагается через SSH-tunnel или Prometheus federation внутри DC.
//
// Метрики регистрируются в default registry через promauto — гарантирует
// отсутствие дублей при повторном импорте пакета и подхватывает рантайм-метрики
// Go (`go_*`, `process_*`) автоматически.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
)

// HTTPRequestsTotal — счётчик HTTP-запросов через Gateway.
//
// Метки `service` и `method` извлекаются из URL после маршрутизации. Чтобы
// `/v1/{rand}/{rand}` не раздувал кардинальность (regex `validSubjectToken`
// разрешает любую пару tokens, не только реальные бэкенды), при `ErrNoResponders`
// после исчерпания retry метки сворачиваются в `service="unknown",method="unknown"`.
// Известные пары без живых подписчиков (rolling deploy) попадают туда же —
// retry-цикл поглощает короткие окна, до метрики ErrNoResponders доходит только
// реально мёртвый subject.
var HTTPRequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_http_requests_total",
		Help: "Total HTTP requests proxied through Gateway by service, method, status.",
	},
	[]string{"service", "method", "status"},
)

// HTTPRequestDuration — гистограмма длительности HTTP-запросов через Gateway
// (от приёма запроса до записи ответа). Buckets — дефолтные prometheus
// (5ms…10s), покрывают типовой диапазон проксирования NATS.
//
// Семантика меток `service`/`method` идентична HTTPRequestsTotal — при
// ErrNoResponders сворачивается в "unknown" (см. описание выше).
var HTTPRequestDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: "gateway_http_request_duration_seconds",
		Help: "End-to-end Gateway HTTP request duration in seconds, by service, method.",
	},
	[]string{"service", "method"},
)

// WSConnectionsActive — текущее число активных WebSocket-соединений через Gateway.
// Gauge, инкрементируется в wsConnGuard при принятии нового подключения,
// декрементируется в release-функции при закрытии.
var WSConnectionsActive = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "gateway_ws_connections_active",
		Help: "Number of currently open WebSocket connections to Gateway.",
	},
)

// RateLimitRejectedTotal — счётчик запросов, отклонённых rate limiter Gateway.
// Метка `kind` различает общий лимит (general) и auth-префикс (auth).
var RateLimitRejectedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_rate_limit_rejected_total",
		Help: "HTTP requests rejected by Gateway rate limiter, by kind (general|auth).",
	},
	[]string{"kind"},
)

// NATSRequestDuration — гистограмма длительности NATS Request-Reply из Gateway
// (включая retry на ErrNoResponders). Метка `service` — префикс subject; при
// финальном ErrNoResponders сворачивается в "unknown" симметрично с HTTPRequestsTotal.
var NATSRequestDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: "nats_request_duration_seconds",
		Help: "Duration of NATS Request-Reply round-trip from Gateway, by service.",
	},
	[]string{"service"},
)

// NATSRequestAttemptsTotal — счётчик попыток NATS-запроса. На один HTTP-запрос
// инкрементируется по числу attempts; outcome фиксирует финальный исход. Метка
// `service` сворачивается в "unknown" при финальном ErrNoResponders (см. HTTPRequestsTotal).
var NATSRequestAttemptsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "nats_request_attempts_total",
		Help: "Total NATS Request-Reply attempts from Gateway, by service, outcome (ok|no_responders|timeout|error).",
	},
	[]string{"service", "outcome"},
)

// Serve запускает Prometheus /metrics endpoint на addr.
// Возвращает http.ErrServerClosed при штатном выключении (Shutdown), любая
// другая ошибка — фатальный bind/network failure.
//
// Вызывается из main в отдельной горутине; metrics-сервер живёт независимо
// от основного HTTP-сервера сервиса, чтобы сбой одного не убил другой.
func Serve(addr string, log zerolog.Logger) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Info().Str("addr", addr).Msg("metrics endpoint запущен")
	return srv.ListenAndServe()
}
