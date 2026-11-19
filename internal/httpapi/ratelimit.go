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

// Keyed by client network (IPv6 /64) plus scope. With shared set, buckets live in Valkey;
// the in-process buckets serve only while Valkey is unreachable.
type rateLimiter struct {
	mu             sync.Mutex
	buckets        map[string]*tokenBucket
	rate           float64 // tokens added per second
	burst          float64
	trustedProxies []*net.IPNet
	scope          rateLimitScope
	// Valkey has one keyspace; name separates limiters in it.
	name   string
	shared *sharedBuckets
}

// Every limiter refills fully within a minute; the security model caps key lifetime at 10 minutes.
const sharedBucketTTL = 2 * time.Minute

// In-cluster round trip is ~1 ms; the margin absorbs a CPU-starved node without a false fallback.
const sharedBucketTimeout = 250 * time.Millisecond

// Uses the Valkey server clock (TIME) so replicas with skewed clocks refill consistently.
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

// Keys are HMAC-SHA-256 under FLICK_RATELIMIT_KEY_SALT; Valkey never holds a raw client network.
type sharedBuckets struct {
	client *redis.Client
	salt   []byte
	// Logs the warning once per outage.
	degraded atomic.Bool
	// Monotonic so a wall-clock jump cannot stretch it.
	retryAt atomic.Int64
}

const sharedBucketRetryAfter = 5 * time.Second

var sharedBucketsEpoch = time.Now()

var errSharedBucketsCoolingDown = errors.New("valkey rate limiter cooling down after a failure")

// Connects lazily so the API boots with Valkey down; never retries so failures fall back at once.
func NewValkeyClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:                  addr,
		DialTimeout:           sharedBucketTimeout,
		ReadTimeout:           sharedBucketTimeout,
		WriteTimeout:          sharedBucketTimeout,
		PoolTimeout:           sharedBucketTimeout,
		ContextTimeoutEnabled: true,
		MaxRetries:            -1,
		// go-redis default (5 attempts, 100ms apart) would spend the whole sharedBucketTimeout on a refused connection.
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

// The context is detached so a client hang-up is not mistaken for a Valkey outage.
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

// A limiter instance is one budget across all its routes, so /api/pipe/X and /api/v1/pipe/X share it.
type rateLimitScope func(*http.Request) string

// Use where rotating IDs must not buy fresh buckets or the route has no ID.
func perNetwork(*http.Request) string { return "" }

func perResourceID(r *http.Request) string { return chi.URLParam(r, "id") }

type tokenBucket struct {
	tokens float64
	last   time.Time
}

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

// Fail-open to the local budget when Valkey is down.
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

// XFF is honored only from a trusted peer, else a rotating XFF bypasses the limiter.
// The right-most non-trusted entry is the client because the trusted hop appends itself.
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

// One IPv6 subscriber usually holds a whole /64; keying by full address would hand it 2^64 buckets.
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

// Invalid entries are skipped so a spoofed XFF cannot mint arbitrary bucket keys.
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
