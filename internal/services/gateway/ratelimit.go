// internal/services/gateway/ratelimit.go
//
// Три уровня ограничений входящего трафика:
//  1. Per-IP HTTP rate limit  — общий лимит на все маршруты /v1/
//  2. Per-IP auth rate limit  — дополнительный жёсткий лимит на настраиваемый
//     URL-префикс (GATEWAY_AUTH_RATE_PREFIX); защита от брутфорса.
//     Если префикс не задан — второй лимит не применяется.
//  3. Глобальный WS-счётчик  — максимум одновременных WebSocket-соединений
//
// Алгоритм: Token Bucket (golang.org/x/time/rate).
// IP-таблица очищается раз в минуту: записи, не активные более 5 минут, удаляются.
package gateway

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
)

// ipEntry — запись в таблице per-IP limiters.
type ipEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// rl — rate limiter Gateway.
// Хранит две независимые таблицы: общую и для auth-маршрутов.
type rl struct {
	mu      sync.Mutex
	general map[string]*ipEntry // общий лимит
	auth    map[string]*ipEntry // дополнительный жёсткий лимит для настраиваемого URL-префикса

	cfg     RateLimitConfig
	wsConns atomic.Int64 // текущее число WS-соединений
	log     zerolog.Logger
}

// newRateLimiter создаёт rate limiter и запускает фоновую очистку таблиц.
// stop — канал завершения; закрывается при штатной остановке Gateway.
func newRateLimiter(cfg RateLimitConfig, log zerolog.Logger, stop <-chan struct{}) *rl {
	r := &rl{
		general: make(map[string]*ipEntry),
		auth:    make(map[string]*ipEntry),
		cfg:     cfg,
		log:     log,
	}
	go r.cleanup(stop)
	return r
}

// allow проверяет общий per-IP лимит. Возвращает false при превышении.
func (r *rl) allow(ip string) bool {
	return r.get(r.general, ip, r.cfg.Rate, r.cfg.Burst).Allow()
}

// allowAuth проверяет auth per-IP лимит. Возвращает false при превышении.
func (r *rl) allowAuth(ip string) bool {
	return r.get(r.auth, ip, r.cfg.AuthRate, r.cfg.AuthBurst).Allow()
}

// get возвращает limiter для IP, создавая его при первом обращении.
func (r *rl) get(table map[string]*ipEntry, ip string, ratePerSec float64, burst int) *rate.Limiter {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := table[ip]
	if !ok {
		e = &ipEntry{limiter: rate.NewLimiter(rate.Limit(ratePerSec), burst)}
		table[ip] = e
	}
	e.lastSeen = time.Now()
	return e.limiter
}

// cleanup удаляет неактивные записи каждую минуту.
// Запись считается неактивной, если с последнего запроса прошло более 5 минут.
func (r *rl) cleanup(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.mu.Lock()
			cutoff := time.Now().Add(-5 * time.Minute)
			for ip, e := range r.general {
				if e.lastSeen.Before(cutoff) {
					delete(r.general, ip)
				}
			}
			for ip, e := range r.auth {
				if e.lastSeen.Before(cutoff) {
					delete(r.auth, ip)
				}
			}
			r.mu.Unlock()
		case <-stop:
			return
		}
	}
}

// =============================================================================
// Middleware и WS-счётчик
// =============================================================================

// middlewareRateLimit применяет per-IP rate limiting к маршрутам /v1/.
//
// Порядок проверок:
//  1. Если GATEWAY_AUTH_RATE_PREFIX задан и путь начинается с него —
//     дополнительно проверяется жёсткий auth-лимит (защита от брутфорса).
//  2. Все маршруты /v1/ проверяются по общему лимиту.
//
// /health не входит в цепочку и не ограничивается.
func (gw *Gateway) middlewareRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := realIP(r, gw.cfg.RateLimit.TrustedProxy)

		// Дополнительный жёсткий лимит для настраиваемого префикса — защита от брутфорса.
		if p := gw.cfg.RateLimit.AuthPathPrefix; p != "" && strings.HasPrefix(r.URL.Path, p) {
			if !gw.rl.allowAuth(ip) {
				gw.log.Warn().Str("ip", ip).Str("path", r.URL.Path).Msg("auth rate limit exceeded")
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "1")
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
		}

		if !gw.rl.allow(ip) {
			gw.log.Warn().Str("ip", ip).Str("path", r.URL.Path).Msg("rate limit exceeded")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// wsConnGuard проверяет глобальный лимит WS-соединений и управляет счётчиком.
// Возвращает false если лимит исчерпан — caller должен вернуть 503.
// При возврате true caller обязан вызвать returned() после закрытия соединения.
func (gw *Gateway) wsConnGuard() (ok bool, release func()) {
	if gw.rl.wsConns.Add(1) > gw.cfg.RateLimit.MaxWSConns {
		gw.rl.wsConns.Add(-1)
		return false, nil
	}
	return true, func() { gw.rl.wsConns.Add(-1) }
}

// realIP извлекает реальный IP клиента.
// X-Real-IP принимается только если запрос пришёл с trustedProxy (Cloudflare, LB).
// Если trustedProxy пустой или RemoteAddr не совпадает — используется r.RemoteAddr.
func realIP(r *http.Request, trustedProxy string) string {
	if trustedProxy != "" {
		remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		if remoteIP == trustedProxy {
			if ip := r.Header.Get("X-Real-IP"); ip != "" {
				return ip
			}
		}
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
