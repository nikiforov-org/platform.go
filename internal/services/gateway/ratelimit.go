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
// Размер таблицы ограничен GATEWAY_RATE_LIMIT_MAX_IPS (по умолчанию 100 000):
// при заполнении самая старая запись вытесняется перед добавлением новой.
package gateway

import (
	"container/list"
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
// elem указывает на собственный node в LRU-списке таблицы:
// при доступе нода передвигается в front, при переполнении удаляется back.
type ipEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
	elem     *list.Element
}

// ipTable — LRU-таблица per-IP limiters: map для O(1) lookup, list для
// O(1) eviction. Инвариант: len(m) == lst.Len(); порядок lst соответствует
// порядку lastSeen — back имеет минимальный lastSeen, front — максимальный.
type ipTable struct {
	m   map[string]*ipEntry
	lst *list.List // Value каждого элемента — string (ip)
}

func newIPTable() *ipTable {
	return &ipTable{
		m:   make(map[string]*ipEntry),
		lst: list.New(),
	}
}

// rl — rate limiter Gateway.
// Хранит две независимые таблицы: общую и для auth-маршрутов.
type rl struct {
	mu      sync.Mutex
	general *ipTable // общий лимит
	auth    *ipTable // дополнительный жёсткий лимит для настраиваемого URL-префикса

	cfg     RateLimitConfig
	wsConns atomic.Int64 // текущее число WS-соединений
	log     zerolog.Logger
}

// newRateLimiter создаёт rate limiter и запускает фоновую очистку таблиц.
// stop — канал завершения; закрывается при штатной остановке Gateway.
func newRateLimiter(cfg RateLimitConfig, log zerolog.Logger, stop <-chan struct{}) *rl {
	r := &rl{
		general: newIPTable(),
		auth:    newIPTable(),
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
// Hit: запись передвигается в front LRU-списка, lastSeen обновляется — O(1).
// Miss при заполненной таблице (>=MaxIPs): из back удаляется самый давно
// использованный IP — O(1) вместо линейного скана. Под DDoS с уникальных
// IP это снимает self-amplification: каждый запрос тратит микросекунды
// под mu независимо от размера таблицы.
func (r *rl) get(table *ipTable, ip string, ratePerSec float64, burst int) *rate.Limiter {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if e, ok := table.m[ip]; ok {
		e.lastSeen = now
		table.lst.MoveToFront(e.elem)
		return e.limiter
	}

	if len(table.m) >= r.cfg.MaxIPs {
		if oldest := table.lst.Back(); oldest != nil {
			oldIP := oldest.Value.(string)
			table.lst.Remove(oldest)
			delete(table.m, oldIP)
		}
	}

	e := &ipEntry{
		limiter:  rate.NewLimiter(rate.Limit(ratePerSec), burst),
		lastSeen: now,
	}
	e.elem = table.lst.PushFront(ip)
	table.m[ip] = e
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
			r.cleanupExpired(r.general, cutoff)
			r.cleanupExpired(r.auth, cutoff)
			r.mu.Unlock()
		case <-stop:
			return
		}
	}
}

// cleanupExpired удаляет с конца LRU-списка все записи с lastSeen < cutoff.
// Back-walk: первая неэкспирированная запись с конца → все более ранние
// тоже свежие (инвариант сортировки). O(K) для K реально просроченных
// вместо O(N). Вызывающий держит r.mu.
func (r *rl) cleanupExpired(table *ipTable, cutoff time.Time) {
	for {
		oldest := table.lst.Back()
		if oldest == nil {
			return
		}
		ip := oldest.Value.(string)
		if !table.m[ip].lastSeen.Before(cutoff) {
			return
		}
		table.lst.Remove(oldest)
		delete(table.m, ip)
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
