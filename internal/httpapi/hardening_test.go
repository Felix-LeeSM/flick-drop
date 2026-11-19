package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Ingress routes /api straight to flick-api, so nginx headers never reach these responses.
func TestResponsesCarryNosniff(t *testing.T) {
	router := newTestRouter(t)

	cases := []struct {
		name, method, path string
	}{
		{"healthz", http.MethodGet, "/healthz"},
		{"config", http.MethodGet, "/api/config"},
		{"missing secret (error body)", http.MethodGet, "/api/secrets/does-not-exist"},
		{"options preflight", http.MethodOptions, "/api/secrets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if got := resp.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff (status %d)", got, resp.Code)
			}
		})
	}
}

// A nil store makes getSecretMetadata panic, exercising the real Recoverer wiring.
func TestRouterRecoversFromHandlerPanic(t *testing.T) {
	router := NewRouter(nil, nil, Options{PayloadInlineMaxBytes: 1024})

	req := httptest.NewRequest(http.MethodGet, "/api/secrets/boom", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 from recovered panic", resp.Code)
	}
	// Recoverer is mounted outside cors, so a recovered 500 still carries nosniff.
	if got := resp.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("recovered-panic response X-Content-Type-Options = %q, want nosniff", got)
	}
}
