// Package ratelimit provides a per-caller token-bucket middleware.
//
// Callers are identified by their authenticated API key when present, falling
// back to the remote IP address. Read methods (GET/HEAD/OPTIONS) and write
// methods (everything else) share separate buckets so a burst of reads cannot
// starve writes and vice versa. The limiter is in-memory; deployments that
// scale beyond a single instance should swap the backing store.
package ratelimit

import (
	"encoding/json"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/nesono/evidence-store/internal/config"
)

// Middleware returns an HTTP middleware that enforces the configured limits.
// If cfg has neither a read nor write RPS set, the returned middleware is a
// no-op.
func Middleware(cfg config.RateLimit) func(http.Handler) http.Handler {
	if !cfg.Enabled() {
		return func(next http.Handler) http.Handler { return next }
	}

	store := newStore(cfg)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limiter := store.limiterFor(callerKey(r), isWrite(r))
			if limiter == nil {
				next.ServeHTTP(w, r)
				return
			}

			reservation := limiter.Reserve()
			if !reservation.OK() {
				writeLimitError(w, 0)
				return
			}
			delay := reservation.Delay()
			if delay > 0 {
				reservation.Cancel()
				writeLimitError(w, delay)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type store struct {
	cfg     config.RateLimit
	mu      sync.Mutex
	readers map[string]*rate.Limiter
	writers map[string]*rate.Limiter
}

func newStore(cfg config.RateLimit) *store {
	return &store{
		cfg:     cfg,
		readers: map[string]*rate.Limiter{},
		writers: map[string]*rate.Limiter{},
	}
}

func (s *store) limiterFor(key string, write bool) *rate.Limiter {
	var rps float64
	var burst int
	if write {
		rps, burst = s.cfg.WriteRPS, s.cfg.WriteBurst
	} else {
		rps, burst = s.cfg.ReadRPS, s.cfg.ReadBurst
	}
	if rps <= 0 || burst <= 0 {
		return nil
	}

	bucket := s.readers
	if write {
		bucket = s.writers
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := bucket[key]; ok {
		return l
	}
	l := rate.NewLimiter(rate.Limit(rps), burst)
	bucket[key] = l
	return l
}

// callerKey returns the bearer token if present, otherwise the remote IP.
// Tokens get a "k:" prefix and IPs an "ip:" prefix so the two namespaces
// cannot collide.
func callerKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) >= len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		if token := strings.TrimSpace(auth[len(prefix):]); token != "" {
			return "k:" + token
		}
	}
	return "ip:" + clientIP(r)
}

func clientIP(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-For"); h != "" {
		if comma := strings.IndexByte(h, ','); comma >= 0 {
			h = h[:comma]
		}
		if ip := strings.TrimSpace(h); ip != "" {
			return ip
		}
	}
	if r.RemoteAddr == "" {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func isWrite(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func writeLimitError(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := max(int(math.Ceil(retryAfter.Seconds())), 1)
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})
}
