package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
)

// rateLimiter is a per-key token bucket limiter. Every bucket key starts with
// the client network (clientNetwork: the client IP, with IPv6 collapsed to its
// /64) and adds the limiter's scope. Each key gets an independent bucket
// refilled at `rate` tokens/second up to `burst`, so one client cannot starve
// unrelated callers through a global quota.
//
// With shared set, buckets live in Valkey so every API replica spends one
// budget per key; the in-process buckets below serve only while Valkey is
// unreachable (sharedBuckets.allow fails), each replica then enforcing its own
// budget.
type rateLimiter struct {
	mu             sync.Mutex
	buckets        map[string]*tokenBucket
	rate           float64 // tokens added per second
	burst          float64
	trustedProxies []*net.IPNet
	scope          rateLimitScope
	// name separates this limiter's Valkey buckets from the other limiters'
	// (each in-process limiter has its own map; Valkey has one keyspace).
	name   string
	shared *sharedBuckets
}

// sharedBucketTTL bounds how long Valkey keeps an idle bucket. Every limiter
// refills its full burst within one minute (rate = burst/60s), so a bucket idle
// longer than that is indistinguishable from a missing one; the security model
// caps rate-limit key lifetime at 10 minutes.
const sharedBucketTTL = 2 * time.Minute

// sharedBucketTimeout bounds one Valkey round trip on the request path. On
// timeout the request is judged by the in-process bucket instead. An in-cluster
// round trip takes about a millisecond; the margin absorbs a CPU-starved node
// without a false fallback, and sharedBucketRetryAfter limits an outage to the
// requests that arrive together once every 5 seconds per replica.
const sharedBucketTimeout = 250 * time.Millisecond

// tokenBucketScript is the in-process allow() token bucket run atomically in
// Valkey. It reads the Valkey server clock (TIME), so replicas with skewed
// clocks still refill one bucket consistently. Returns 1 when a token was
// spent, 0 when the bucket is empty.
var tokenBucketScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) + tonumber(t[2]) / 1000000
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local state = redis.call('HMGET', KEYS[1], 'tokens', 'last')
local tokens = tonumber(state[1])
local last = tonumber(state[2])
if tokens == nil or last == nil then
  tokens = burst
  last = now
end
local elapsed = now - last
if elapsed > 0 then
  tokens = math.min(burst, tokens + elapsed * rate)
end
local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end
redis.call('HSET', KEYS[1], 'tokens', string.format('%.6f', tokens), 'last', string.format('%.6f', now))
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[3]))
return allowed
`)

// sharedBuckets stores token buckets in Valkey. Keys are HMAC-SHA-256 digests
// of the limiter name and bucket key under the deployment salt
// (FLICK_RATELIMIT_KEY_SALT), so Valkey never holds a raw client network
// (docs/architecture/security-model.md, "Valkey").
type sharedBuckets struct {
	client *redis.Client
	salt   []byte
	// degraded is true while Valkey calls fail, so the warning logs once per
	// outage instead of once per request.
	degraded atomic.Bool
	// retryAt (monotonic nanoseconds since sharedBucketsEpoch, so a wall-clock
	// jump cannot stretch it) skips Valkey for sharedBucketRetryAfter after
	// a failure, so an unreachable Valkey does not add sharedBucketTimeout to
	// every request of the outage.
	retryAt atomic.Int64
}

// sharedBucketRetryAfter is how long the limiters stay on in-process buckets
// after a Valkey failure before trying Valkey again.
const sharedBucketRetryAfter = 5 * time.Second

// sharedBucketsEpoch anchors retryAt on the monotonic clock.
var sharedBucketsEpoch = time.Now()

var errSharedBucketsCoolingDown = errors.New("valkey rate limiter cooling down after a failure")

// NewValkeyClient builds the Valkey client the rate limiters share
// (FLICK_VALKEY_ADDR). It connects lazily, so the API boots while Valkey is
// down, and it never retries: a failed call falls back to the in-process
// bucket at once instead of holding the request.
func NewValkeyClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:                  addr,
		DialTimeout:           sharedBucketTimeout,
		ReadTimeout:           sharedBucketTimeout,
		WriteTimeout:          sharedBucketTimeout,
		PoolTimeout:           sharedBucketTimeout,
		ContextTimeoutEnabled: true,
		MaxRetries:            -1,
		// One dial attempt: the go-redis default (5 attempts, 100ms apart)
		// would spend the whole sharedBucketTimeout on a refused connection.
		DialerRetries: 1,
	})
}

func newSharedBuckets(client *redis.Client, salt string) *sharedBuckets {
	return &sharedBuckets{client: client, salt: []byte(salt)}
}

func (s *sharedBuckets) key(limiter, bucket string) string {
	mac := hmac.New(sha256.New, s.salt)
	mac.Write([]byte(limiter))
	mac.Write([]byte{0})
	mac.Write([]byte(bucket))
	return "rl:" + hex.EncodeToString(mac.Sum(nil))
}

// allow spends one token from the shared bucket. An error means the caller
// must judge the request itself: Valkey failed now, or failed less than
// sharedBucketRetryAfter ago. The context is detached from the request so a
// client hang-up is not mistaken for a Valkey outage.
func (s *sharedBuckets) allow(ctx context.Context, rl *rateLimiter, bucket string) (bool, error) {
	now := int64(time.Since(sharedBucketsEpoch))
	if now < s.retryAt.Load() {
		return false, errSharedBucketsCoolingDown
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedBucketTimeout)
	defer cancel()
	allowed, err := tokenBucketScript.Run(ctx, s.client, []string{s.key(rl.name, bucket)},
		rl.rate, rl.burst, int(sharedBucketTTL.Seconds())).Int()
	if err != nil {
		s.retryAt.Store(now + int64(sharedBucketRetryAfter))
		if s.degraded.CompareAndSwap(false, true) {
			slog.Warn("valkey rate limiter unavailable; falling back to per-process buckets", "error", err)
		}
		return false, err
	}
	if s.degraded.CompareAndSwap(true, false) {
		slog.Info("valkey rate limiter recovered; buckets are shared across replicas again")
	}
	return allowed == 1, nil
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
		if !rl.allowRequest(r.Context(), rl.clientNetwork(r)+"|"+rl.scope(r)) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowRequest judges one request: by the shared Valkey bucket when one is
// configured and reachable, otherwise by this process's bucket. Valkey being
// down never blocks a request outright (fail-open to the local budget).
func (rl *rateLimiter) allowRequest(ctx context.Context, bucket string) bool {
	if rl.shared != nil {
		allowed, err := rl.shared.allow(ctx, rl, bucket)
		if err == nil {
			return allowed
		}
		telemetry.RateLimitFallbacks.WithLabelValues(rl.name).Inc()
	}
	return rl.allow(bucket, time.Now().UTC())
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
