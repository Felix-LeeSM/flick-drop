package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestGetConfig(t *testing.T) {
	fixture := newTestRouterFixture(t, Options{
		PayloadInlineMaxBytes: 2048,
		MaxFileBytes:          1_000_000,
	})

	resp := performJSON(t, fixture.router, http.MethodGet, "/api/config", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	if got := resp.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Fatalf("Cache-Control = %q, want %q", got, "public, max-age=300")
	}

	var body configResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if body.PayloadInlineMaxBytes != 2048 {
		t.Fatalf("payload_inline_max_bytes = %d, want 2048", body.PayloadInlineMaxBytes)
	}
	if body.MaxFileBytes != 1_000_000 {
		t.Fatalf("max_file_bytes = %d, want 1000000", body.MaxFileBytes)
	}
}

func TestGetConfigFallsBackToDefaultsWhenUnset(t *testing.T) {
	// An empty Options leaves both limits at zero, so the router must substitute
	// the built-in defaults (1 MiB inline threshold, 50 MiB absolute bound).
	fixture := newTestRouterFixture(t, Options{})

	resp := performJSON(t, fixture.router, http.MethodGet, "/api/config", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}

	var body configResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if body.PayloadInlineMaxBytes != 1048576 {
		t.Fatalf("payload_inline_max_bytes = %d, want 1048576 (default fallback)", body.PayloadInlineMaxBytes)
	}
	if body.MaxFileBytes != 52428800 {
		t.Fatalf("max_file_bytes = %d, want 52428800 (default fallback)", body.MaxFileBytes)
	}
}
