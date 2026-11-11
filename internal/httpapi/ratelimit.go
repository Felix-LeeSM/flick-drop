package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// rateLimiter is a per-key token bucket limiter with no external dependencies.
// Every bucket key starts with the client network (clientNetwork: the client
// IP, with IPv6 collapsed to its /64) and adds the limiter's scope. Each key
// gets an independent bucket refilled at `rate` tokens/second up to `burst`, so
// one client cannot starve unrelated callers through a global quota.
type rateLimiter struct {
	mu             sync.Mutex
	buckets        map[string]*tokenBucket
	rate           float64 // tokens added per second
	burst          float64
	trustedProxies []*net.IPNet
	scope          rateLimitScope
}

// rateLimitScope picks which requests from one client network spend the same
// bucket. A limiter instance is one budget: every route mounted with it shares
// the bucket the scope selects, whatever the route's path, so /api/pipe/X and
// /api/v1/pipe/X never get two budgets.
type rateLimitScope func(*http.Request) string

// perNetwork gives each client network one bucket for every route on the
// limiter. Use it where rotating IDs must not buy fresh buckets (request link
// calls, code joins) or where the route has no ID (creation, pipe init).
func perNetwork(*http.Request) string { return "" }

// perResourceID gives each client network one bucket per `{id}` route
// parameter: guesses against one secret are bounded without a client's
// activity on one secret slowing its access to another.
func perResourceID(r *http.Request) string { return chi.URLParam(r, "id") }

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// newRateLimiter builds a limiter allowing perMinute requests per key per
// minute (burst == per-minute allowance), keyed by client network plus scope.
// trustedProxies gates which direct peers may supply a client IP via
// X-Forwarded-For; an empty set means only the direct RemoteAddr is ever used.
// A background goroutine evicts idle buckets.
func newRateLimiter(perMinute int, trustedProxies []*net.IPNet, scope rateLimitScope) *rateLimiter {
	if perMinute <= 0 {
		perMinute = 10
	}
	rl := &rateLimiter{
		buckets:        make(map[string]*tokenBucket),
		rate:           float64(perMinute) / 60.0,
		burst:          float64(perMinute),
		trustedProxies: trustedProxies,
		scope:          scope,
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

// middleware spends one token from the bucket of the request's client network
// and scope. Requests over the limit get 429 with a Retry-After hint.
func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(rl.clientNetwork(r)+"|"+rl.scope(r), time.Now().UTC()) {
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
// value walking right-to-left is the original client. If XFF holds no valid
// non-trusted IP, the peer is used.
func (rl *rateLimiter) clientIP(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if isTrustedIP(peer, rl.trustedProxies) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if ip := rightmostUntrusted(xff, rl.trustedProxies); ip != "" {
				return ip
			}
		}
	}
	return peer
}

// clientNetwork is clientIP with an IPv6 address collapsed to its /64 prefix
// (for example "2001:db8:1:2::/64"). One IPv6 subscriber usually holds a whole
// /64, so keying by the full address would hand it 2^64 fresh buckets. IPv4
// addresses (including IPv4-mapped IPv6) and unparsable values pass through
// unchanged.
func (rl *rateLimiter) clientNetwork(r *http.Request) string {
	ip := rl.clientIP(r)
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
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
// the first entry that is a valid, non-trusted IP. Entries that are empty or
// not a valid IP are skipped, so a spoofed XFF cannot mint arbitrary bucket
// keys (which would let an attacker rotate a fresh limiter bucket per request
// and bypass the rate limit). If no valid non-trusted entry exists, it
// returns "" and the caller falls back to the direct peer.
func rightmostUntrusted(xff string, trusted []*net.IPNet) string {
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		if candidate == "" || net.ParseIP(candidate) == nil {
			continue
		}
		if !isTrustedIP(candidate, trusted) {
			return candidate
		}
	}
	return ""
}
