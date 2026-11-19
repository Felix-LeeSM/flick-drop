package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
)

func openTestValkey(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("FLICK_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("FLICK_TEST_VALKEY_ADDR is not set; Valkey-backed rate limiter tests run only against the compose/CI service")
	}
	client := NewValkeyClient(addr)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func randomSalt(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func replicaLimiter(client *redis.Client, salt, name string, perMinute int) *rateLimiter {
	rl := newRateLimiter(perMinute, nil, perNetwork)
	rl.name, rl.shared = name, newSharedBuckets(client, salt)
	return rl
}

func limitedStatus(rl *rateLimiter, remoteAddr string) int {
	handler := rl.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/secrets", nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}

func TestSharedRateLimiterSpendsOneBudgetAcrossReplicas(t *testing.T) {
	salt := randomSalt(t)
	replicaA := replicaLimiter(openTestValkey(t), salt, "secret_create", 3)
	replicaB := replicaLimiter(openTestValkey(t), salt, "secret_create", 3)
	other := replicaLimiter(openTestValkey(t), salt, "request_create", 3)

	for i, rl := range []*rateLimiter{replicaA, replicaB, replicaA} {
		if got := limitedStatus(rl, "203.0.113.7:1000"); got != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i+1, got)
		}
	}
	if got := limitedStatus(replicaB, "203.0.113.7:1001"); got != http.StatusTooManyRequests {
		t.Fatalf("4th request on the other replica = %d, want 429 (one shared budget)", got)
	}
	if got := limitedStatus(other, "203.0.113.7:1002"); got != http.StatusOK {
		t.Fatalf("different limiter = %d, want 200 (separate budget)", got)
	}
	if got := limitedStatus(replicaA, "198.51.100.9:1000"); got != http.StatusOK {
		t.Fatalf("different client network = %d, want 200", got)
	}
}

func TestSharedRateLimiterStoresHashedKeysWithBoundedTTL(t *testing.T) {
	ctx := context.Background()
	client := openTestValkey(t)
	salt := randomSalt(t)
	rl := replicaLimiter(client, salt, "secret_create", 3)
	const ip = "203.0.113.77"

	if got := limitedStatus(rl, ip+":1000"); got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}

	key := rl.shared.key("secret_create", ip+"|")
	ttl, err := client.TTL(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 || ttl > 10*time.Minute {
		t.Fatalf("bucket TTL = %s, want within (0, 10m]", ttl)
	}
	fields, err := client.HGetAll(ctx, key).Result()
	if err != nil || len(fields) == 0 {
		t.Fatalf("bucket fields = %v, %v", fields, err)
	}
	keys, err := client.Keys(ctx, "*").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if strings.Contains(k, ip) || strings.Contains(k, salt) {
			t.Fatalf("valkey key %q exposes the client network or salt", k)
		}
	}
	for field, value := range fields {
		if strings.Contains(field+value, ip) {
			t.Fatalf("bucket field %s=%s exposes the client network", field, value)
		}
	}
}

func TestSharedRateLimiterRefills(t *testing.T) {
	rl := replicaLimiter(openTestValkey(t), randomSalt(t), "pipe_status", 60)
	for i := range 60 {
		if got := limitedStatus(rl, "203.0.113.8:1000"); got != http.StatusOK {
			t.Fatalf("burst request %d = %d, want 200", i+1, got)
		}
	}
	if got := limitedStatus(rl, "203.0.113.8:1000"); got != http.StatusTooManyRequests {
		t.Fatalf("over burst = %d, want 429", got)
	}
	time.Sleep(1100 * time.Millisecond)
	if got := limitedStatus(rl, "203.0.113.8:1000"); got != http.StatusOK {
		t.Fatalf("after one refill interval = %d, want 200", got)
	}
	if got := limitedStatus(rl, "203.0.113.8:1000"); got != http.StatusTooManyRequests {
		t.Fatalf("refill granted more than one token = %d, want 429", got)
	}
}

func TestRateLimiterFallsBackToProcessBucketsWhenValkeyDown(t *testing.T) {
	down := NewValkeyClient("127.0.0.1:1") // 아무도 듣지 않는 포트
	t.Cleanup(func() { _ = down.Close() })
	rl := replicaLimiter(down, randomSalt(t), "fallback_test", 2)
	before := testutil.ToFloat64(telemetry.RateLimitFallbacks.WithLabelValues("fallback_test"))

	want := []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests}
	start := time.Now()
	for i, code := range want {
		if got := limitedStatus(rl, "203.0.113.9:1000"); got != code {
			t.Fatalf("request %d with valkey down = %d, want %d", i+1, got, code)
		}
	}
	if elapsed := time.Since(start); elapsed >= 2*sharedBucketTimeout {
		t.Fatalf("3 requests with valkey down took %s, want < %s", elapsed, 2*sharedBucketTimeout)
	}
	if got := testutil.ToFloat64(telemetry.RateLimitFallbacks.WithLabelValues("fallback_test")) - before; got != 3 {
		t.Fatalf("fallback counter grew by %v, want 3", got)
	}
}

func TestRouterServesWithValkeyDown(t *testing.T) {
	down := NewValkeyClient("127.0.0.1:1") // 아무도 듣지 않는 포트
	t.Cleanup(func() { _ = down.Close() })
	router := newTestRouterWithOptions(t, Options{
		PayloadInlineMaxBytes: 1024,
		NATSConnected:         func() bool { return true },
		Valkey:                down,
		RateLimitKeySalt:      randomSalt(t),
	})

	if resp := performJSON(t, router, http.MethodGet, "/readyz", nil); resp.Code != http.StatusOK {
		t.Fatalf("readyz with valkey down = %d, want 200, body = %s", resp.Code, resp.Body.String())
	}
	if resp := performJSON(t, router, http.MethodPost, "/api/secrets", validCreateSecretBody()); resp.Code != http.StatusCreated {
		t.Fatalf("create with valkey down = %d, want 201, body = %s", resp.Code, resp.Body.String())
	}
}
