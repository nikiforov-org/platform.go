// internal/platform/nc/health.go
//
// Health-probe endpoint для NATS-only сервисов.
//
// Nomad по умолчанию считает аллокацию healthy сразу после старта процесса.
// TCP/PING от NATS не ловит deadlock-в-handler: PING обрабатывается отдельной
// горутиной nats.go, NATS-сервер считает клиента online, Nomad без HTTP-probe
// не видит зависшего dispatcher'а. Чтобы поймать такой deadlock, probe обязан
// идти через тот же mux, что и бизнес-handler'ы — отдельный self-NATS-roundtrip.
//
// RegisterHealth поднимает:
//  1. Подписку на `_health.<svc>.<nuid>` через основной nats.Conn (тот же
//     dispatcher, что у бизнес-подписок) — отвечает пустой строкой.
//  2. HTTP-сервер на HEALTH_ADDR с `/healthz`, который делает self-request
//     на `_health.<svc>.<nuid>` с таймаутом 1s. Ответ 200 — handler живой,
//     503 — deadlock/disconnect.
//
// Subject включает nuid — уникален per-instance, параллельные инстансы
// не пересекаются; queue-group не используется (каждый пингует сам себя).
package nc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nuid"
	"github.com/rs/zerolog"
)

// HealthEndpoint — запущенные NATS-подписка `_health.<svc>.<nuid>`
// и HTTP-сервер `/healthz`. Shutdown обязан вызываться ДО Drain основного
// NATS-клиента: сначала /healthz начинает отдавать 503 (Nomad снимает
// аллокацию с балансировки), потом дренируем бизнес-подписки.
type HealthEndpoint struct {
	sub    *nats.Subscription
	server *http.Server
	log    zerolog.Logger
}

const healthProbeTimeout = 1 * time.Second

// RegisterHealth регистрирует health-probe для сервиса svc на addr (`host:port`).
// Возвращает HealthEndpoint — вызвать Shutdown ДО Drain основного NATS-клиента.
func (p *PlatformClient) RegisterHealth(svc, addr string) (*HealthEndpoint, error) {
	if svc == "" {
		return nil, errors.New("nc.RegisterHealth: svc пустой")
	}
	if addr == "" {
		return nil, errors.New("nc.RegisterHealth: addr пустой")
	}

	// nuid гарантирует уникальность subject'а между инстансами одного сервиса —
	// self-request приходит именно в эту подписку, а не в соседний инстанс.
	subject := fmt.Sprintf("_health.%s.%s", svc, nuid.Next())

	sub, err := p.Conn.Subscribe(subject, func(msg *nats.Msg) {
		// Отвечаем пустым payload — факт доставки ответа достаточен:
		// dispatcher не deadlock'нут, mux обрабатывает сообщения.
		if err := msg.Respond(nil); err != nil {
			p.log.Warn().Err(err).Str("subject", subject).Msg("health: respond failed")
		}
	})
	if err != nil {
		return nil, fmt.Errorf("nc.RegisterHealth: subscribe %q: %w", subject, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthProbeTimeout)
		defer cancel()

		_, reqErr := p.Conn.RequestWithContext(ctx, subject, nil)
		if reqErr == nil {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Различаем причины: деталь для триажа, не шумим в штатном shutdown.
		switch {
		case errors.Is(reqErr, nats.ErrNoResponders):
			p.log.Warn().Err(reqErr).Str("subject", subject).Msg("health: no responders")
		case errors.Is(reqErr, context.DeadlineExceeded):
			p.log.Error().Err(reqErr).Str("subject", subject).Msg("health: dispatcher deadlock")
		case errors.Is(reqErr, nats.ErrConnectionClosed):
			p.log.Debug().Str("subject", subject).Msg("health: connection closed (shutdown)")
		default:
			p.log.Error().Err(reqErr).Str("subject", subject).Msg("health: probe failed")
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Слушатель поднимаем синхронно — если порт занят, возвращаем ошибку
	// сразу, не маскируя её в фоновой горутине.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		_ = sub.Unsubscribe()
		return nil, fmt.Errorf("nc.RegisterHealth: listen %q: %w", addr, err)
	}

	h := &HealthEndpoint{sub: sub, server: server, log: p.log}

	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.log.Error().Err(err).Str("addr", addr).Msg("health: HTTP server stopped")
		}
	}()

	p.log.Info().Str("subject", subject).Str("addr", addr).Msg("health: запущен")
	return h, nil
}

// Shutdown останавливает HTTP-сервер и отписывает health-подписку.
// Вызывать ДО Drain основного NATS-клиента: после Shutdown /healthz
// отдаёт connection-refused, Nomad снимает аллокацию с балансировки,
// затем штатно дренируем бизнес-подписки.
func (h *HealthEndpoint) Shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	var firstErr error
	if err := h.server.Shutdown(ctx); err != nil {
		firstErr = fmt.Errorf("nc.HealthEndpoint.Shutdown: http: %w", err)
	}
	if err := h.sub.Unsubscribe(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("nc.HealthEndpoint.Shutdown: unsubscribe: %w", err)
	}
	return firstErr
}
