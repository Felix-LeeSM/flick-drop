package httpapi

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func mustCIDR(t *testing.T, raw string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(raw)
	if err != nil {
		t.Fatalf("parse cidr %s: %v", raw, err)
	}
	return n
}

func TestRateLimiterAllowsBurstThenBlocks(t *testing.T) {
	rl := newRateLimiter(2, nil, perNetwork)
	now := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	if !rl.allow("k", now) {
		t.Fatal("1st allow")
	}
	if !rl.allow("k", now) {
		t.Fatal("2nd allow")
	}
	if rl.allow("k", now) {
		t.Fatal("3rd must be blocked over burst")
	}
}

func TestRateLimiterKeysAreIndependent(t *testing.T) {
	rl := newRateLimiter(1, nil, perNetwork)
	now := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	if !rl.allow("ip-a|/open", now) {
		t.Fatal("first key allowed")
	}
	if !rl.allow("ip-b|/open", now) {
		t.Fatal("second IP independent")
	}
	if !rl.allow("ip-a|/other", now) {
		t.Fatal("different path = different key")
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	// Drain to zero first so the refill branch is actually exercised.
	rl := newRateLimiter(2, nil, perNetwork)
	now := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	if !rl.allow("k", now) {
		t.Fatal("1st")
	}
	if !rl.allow("k", now) {
		t.Fatal("2nd")
	}
	if rl.allow("k", now) {
		t.Fatal("3rd must be blocked, bucket empty")
	}
	if rl.allow("k", now.Add(15*time.Second)) {
		t.Fatal("must stay blocked before refill completes")
	}
	if !rl.allow("k", now.Add(30*time.Second)) {
		t.Fatal("expected allow after one refill window")
	}
}

func TestClientIPHonorsXFFOnlyFromTrustedPeer(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.1/32")}
	rl := newRateLimiter(10, trusted, perNetwork)

	r := httptest.NewRequest(http.MethodPost, "/api/secrets/x/open", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 127.0.0.1")
	if got := rl.clientIP(r); got != "203.0.113.5" {
		t.Fatalf("trusted peer XFF: got %q, want 203.0.113.5", got)
	}

	r2 := httptest.NewRequest(http.MethodPost, "/api/secrets/x/open", nil)
	r2.RemoteAddr = "198.51.100.7:5555"
	r2.Header.Set("X-Forwarded-For", "203.0.113.5")
	if got := rl.clientIP(r2); got != "198.51.100.7" {
		t.Fatalf("untrusted peer: got %q, want 198.51.100.7", got)
	}

	rlNone := newRateLimiter(10, nil, perNetwork)
	r3 := httptest.NewRequest(http.MethodPost, "/api/secrets/x/open", nil)
	r3.RemoteAddr = "198.51.100.9:5555"
	r3.Header.Set("X-Forwarded-For", "203.0.113.5")
	if got := rlNone.clientIP(r3); got != "198.51.100.9" {
		t.Fatalf("no trusted proxies: got %q, want 198.51.100.9", got)
	}
}

func TestClientIPSkipsNonIPXFFEntries(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.1/32")}
	rl := newRateLimiter(10, trusted, perNetwork)

	// A non-IP XFF must never become a bucket key (memory-DoS vector).
	r := httptest.NewRequest(http.MethodPost, "/api/secrets/x/open", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "not-an-ip, !!!, garbage-string")
	if got := rl.clientIP(r); got != "127.0.0.1" {
		t.Fatalf("non-IP XFF should fall back to peer: got %q, want 127.0.0.1", got)
	}

	r2 := httptest.NewRequest(http.MethodPost, "/api/secrets/x/open", nil)
	r2.RemoteAddr = "127.0.0.1:1234"
	r2.Header.Set("X-Forwarded-For", "203.0.113.5, garbage, 127.0.0.1")
	if got := rl.clientIP(r2); got != "203.0.113.5" {
		t.Fatalf("valid IP should win over non-IP junk: got %q, want 203.0.113.5", got)
	}

	r3 := httptest.NewRequest(http.MethodPost, "/api/secrets/x/open", nil)
	r3.RemoteAddr = "127.0.0.1:1234"
	r3.Header.Set("X-Forwarded-For", "  ,  ,  ")
	if got := rl.clientIP(r3); got != "127.0.0.1" {
		t.Fatalf("empty XFF entries should fall back to peer: got %q, want 127.0.0.1", got)
	}
}

func TestClientNetworkCollapsesIPv6ToSlash64AndKeepsIPv4(t *testing.T) {
	rl := newRateLimiter(10, nil, perNetwork)
	for remoteAddr, want := range map[string]string{
		"[2001:db8:1:2:aaaa::1]:443":  "2001:db8:1:2::/64",
		"[2001:db8:1:2:ffff::ff]:443": "2001:db8:1:2::/64",
		"[2001:db8:1:3::1]:443":       "2001:db8:1:3::/64",
		"203.0.113.5:443":             "203.0.113.5",
		"[::ffff:203.0.113.5]:443":    "::ffff:203.0.113.5",
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/pipe/join", nil)
		r.RemoteAddr = remoteAddr
		if got := rl.clientNetwork(r); got != want {
			t.Errorf("clientNetwork(%s) = %q, want %q", remoteAddr, got, want)
		}
	}
}

func TestRateLimitsKeyIPv6ByNetworkOnEveryScope(t *testing.T) {
	f := newTestRouterFixture(t, Options{OpenRatePerMinute: 1, CreateRatePerMinute: 1})
	from := func(method, path, remoteAddr string, body []byte) int {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, r)
		return rec.Code
	}
	createBody, err := json.Marshal(validCreateSecretBody())
	if err != nil {
		t.Fatal(err)
	}

	if code := from(http.MethodPost, "/api/secrets", "[2001:db8:1:2::1]:443", createBody); code == http.StatusTooManyRequests {
		t.Fatal("첫 생성은 허용되어야 한다")
	}
	if code := from(http.MethodPost, "/api/secrets", "[2001:db8:1:2::2]:443", createBody); code != http.StatusTooManyRequests {
		t.Fatalf("같은 /64의 두 번째 생성 = %d, want 429", code)
	}
	if code := from(http.MethodPost, "/api/secrets", "[2001:db8:1:3::1]:443", createBody); code == http.StatusTooManyRequests {
		t.Fatal("다른 /64는 자기 버킷을 가져야 한다")
	}

	if code := from(http.MethodPost, "/api/secrets/id-a/open", "[2001:db8:1:2::1]:443", []byte(`{}`)); code == http.StatusTooManyRequests {
		t.Fatal("첫 열람은 허용되어야 한다")
	}
	if code := from(http.MethodPost, "/api/secrets/id-a/open", "[2001:db8:1:2::2]:443", []byte(`{}`)); code != http.StatusTooManyRequests {
		t.Fatalf("같은 /64·같은 ID의 두 번째 열람 = %d, want 429", code)
	}
	if code := from(http.MethodPost, "/api/secrets/id-b/open", "[2001:db8:1:2::2]:443", []byte(`{}`)); code == http.StatusTooManyRequests {
		t.Fatal("다른 ID는 자기 버킷을 가져야 한다")
	}
}
