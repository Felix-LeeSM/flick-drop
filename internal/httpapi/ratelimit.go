package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// rateLimiter is a per-key token bucket limiter with no external dependencies.
// Each key (client IP + request path) gets an independent bucket refilled at
// `rate` tokens/second up to `burst`. This protects the open endpoint against
// brute-force access-proof guessing without coupling to a global quota that
// could starve unrelated callers.
type rateLimiter struct {
	mu             sync.Mutex
	buckets        map[string]*tokenBucket
	rate           float64 // tokens added per second
	burst          float64
	trustedProxies []*net.IPNet
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// newRateLimiter builds a limiter allowing perMinute requests per key per
// minute (burst == per-minute allowance). trustedProxies gates which direct
// peers may supply a client IP via X-Forwarded-For; an empty set means only the
// direct RemoteAddr is ever used. A background goroutine evicts idle buckets.
func newRateLimiter(perMinute int, trustedProxies []*net.IPNet) *rateLimiter {
	if perMinute <= 0 {
		perMinute = 10
	}
	rl := &rateLimiter{
		buckets:        make(map[string]*tokenBucket),
		rate:           float64(perMinute) / 60.0,
		burst:          float64(perMinute),
		trustedProxies: trustedProxies,
	}
	go rl.cleanup()
	return rl
}

// allow reports whether key may proceed at now, consuming one token if so.
func (rl *rateLimiter) allow(key string, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * rl.rate
		if b.tokens > rl.burst {
			b.tokens = rl.burst
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// cleanup drops buckets untouched for 10 minutes, every 5 minutes, for the
// lifetime of the process.
func (rl *rateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for now := range ticker.C {
		rl.mu.Lock()
		for key, b := range rl.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(rl.buckets, key)
			}
		}
		rl.mu.Unlock()
	}
}

// middleware applies the limiter keyed by client IP + request path. Requests
// over the limit get 429 with a Retry-After hint.
func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := rl.clientIP(r) + "|" + r.URL.Path
		if !rl.allow(key, time.Now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP resolves the originating client address. The direct peer must be a
// configured trusted proxy before X-Forwarded-For is honored at all (otherwise
// an attacker rotating a client-supplied XFF would get a fresh bucket per
// request and bypass the limiter). When honored, the right-most non-trusted
// entry is taken — the trusted hop appends itself, so the first non-trusted
// value walking right-to-left is the original client.
func (rl *rateLimiter) clientIP(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if isTrustedIP(peer, rl.trustedProxies) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return rightmostUntrusted(xff, rl.trustedProxies)
		}
	}
	return peer
}

func isTrustedIP(ip string, trusted []*net.IPNet) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, cidr := range trusted {
		if cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

// rightmostUntrusted walks the X-Forwarded-For chain right-to-left and returns
// the first entry that is not a trusted proxy. If every entry is trusted it
// falls back to the left-most (first) entry.
func rightmostUntrusted(xff string, trusted []*net.IPNet) string {
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		if !isTrustedIP(candidate, trusted) {
			return candidate
		}
	}
	return strings.TrimSpace(parts[0])
}
