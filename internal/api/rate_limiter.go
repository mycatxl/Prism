package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// RateLimiter implements a simple token bucket rate limiter per IP address.
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	limit    int
	window   time.Duration
	cleanupT *time.Ticker
	done     chan struct{}
}

type bucket struct {
	tokens   int
	lastSeen time.Time
}

// NewRateLimiter creates a rate limiter that allows 'limit' requests per 'window' per IP.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		buckets: make(map[string]*bucket),
		limit:   limit,
		window:  window,
		done:    make(chan struct{}),
	}
	rl.cleanupT = time.NewTicker(window)
	go rl.cleanup()
	return rl
}

// Allow checks if a request from the given IP should be allowed.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, exists := rl.buckets[ip]
	if !exists {
		rl.buckets[ip] = &bucket{tokens: rl.limit - 1, lastSeen: now}
		return true
	}

	elapsed := now.Sub(b.lastSeen)
	if elapsed >= rl.window {
		b.tokens = rl.limit - 1
		b.lastSeen = now
		return true
	}

	if b.tokens > 0 {
		b.tokens--
		b.lastSeen = now
		return true
	}

	b.lastSeen = now
	return false
}

// cleanup removes stale buckets to prevent unbounded memory growth.
func (rl *RateLimiter) cleanup() {
	for {
		select {
		case <-rl.cleanupT.C:
			rl.mu.Lock()
			now := time.Now()
			for ip, b := range rl.buckets {
				if now.Sub(b.lastSeen) > 2*rl.window {
					delete(rl.buckets, ip)
				}
			}
			rl.mu.Unlock()
		case <-rl.done:
			return
		}
	}
}

// Close stops the rate limiter cleanup goroutine.
func (rl *RateLimiter) Close() {
	close(rl.done)
	rl.cleanupT.Stop()
}

// RateLimitMiddleware returns middleware that rate limits requests by IP address.
// It applies the rate limiter to all requests passing through.
func RateLimitMiddleware(limiter *RateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)
		if !limiter.Allow(ip) {
			WriteError(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests, please try again later")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractIP extracts the client IP from the request.
func extractIP(r *http.Request) string {
	// Try X-Forwarded-For first (for proxied requests)
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		// Take the first IP in the list
		for idx := 0; idx < len(xff); idx++ {
			if xff[idx] == ',' {
				xff = xff[:idx]
				break
			}
		}
		if ip := net.ParseIP(xff); ip != nil {
			return ip.String()
		}
	}

	// Try X-Real-IP
	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		if ip := net.ParseIP(xri); ip != nil {
			return ip.String()
		}
	}

	// Fall back to RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
