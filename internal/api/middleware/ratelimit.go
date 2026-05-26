package middleware

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter returns a per-IP token-bucket middleware.
// rps — sustained requests per second; burst — maximum burst size.
// Stale entries (no requests for ttl) are purged on each new request.
func RateLimiter(rps float64, burst int, ttl time.Duration) func(http.Handler) http.Handler {
	var (
		mu      sync.Mutex
		clients = make(map[string]*ipLimiter)
	)

	// cleanupLocked purges stale entries; must be called with mu held.
	cleanupLocked := func() {
		for ip, cl := range clients {
			if time.Since(cl.lastSeen) > ttl {
				delete(clients, ip)
			}
		}
	}

	getLimiter := func(ip string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		cleanupLocked()
		cl, ok := clients[ip]
		if !ok {
			cl = &ipLimiter{limiter: rate.NewLimiter(rate.Limit(rps), burst)}
			clients[ip] = cl
		}
		cl.lastSeen = time.Now()
		return cl.limiter
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if !getLimiter(ip).Allow() {
				http.Error(w, `{"error":"too many requests"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}
