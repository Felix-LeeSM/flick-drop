package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Every API response — including error bodies and the OPTIONS preflight — must
// carry X-Content-Type-Options: nosniff. The ingress routes /api straight to
// flick-api, so the nginx headers that cover the web app never reach these
// responses; the cors middleware is the only unconditional per-response hook.
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

// A handler panic must surface as a recovered 500, not a dropped connection. A
// nil store makes getSecretMetadata nil-dereference, exercising the real
// middleware.Recoverer wiring NewRouter installs (ServeHTTP must return rather
// than let the panic escape).
func TestRouterRecoversFromHandlerPanic(t *testing.T) {
	router := NewRouter(nil, nil, Options{PayloadInlineMaxBytes: 1024})

	req := httptest.NewRequest(http.MethodGet, "/api/secrets/boom", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 from recovered panic", resp.Code)
	}
}
