package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/requests"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var requestKeyOnce sync.Once
var requestTestKey string

func requestFixture(t *testing.T, rate int) (http.Handler, *requests.Store, *sql.DB) {
	t.Helper()
	conn := openHTTPTestDB(t, context.Background())
	store, err := requests.NewStore(conn, requests.Options{PayloadInlineMaxBytes: 1024, MaxFileBytes: 900, MinTTLSeconds: 300, DefaultTTLSeconds: 600, MaxTTLSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(conn, nil, Options{RequestStore: store, PayloadInlineMaxBytes: 1024, OpenRatePerMinute: rate, CreateRatePerMinute: rate, AllowedOrigin: "https://web.example"}), store, conn
}

func createHTTPRequest(t *testing.T, router http.Handler) requests.Created {
	t.Helper()
	requestKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		requestTestKey = base64.StdEncoding.EncodeToString(der)
	})
	resp := performJSON(t, router, http.MethodPost, "/api/requests", map[string]any{"public_key": requestTestKey})
	assertRequestStatus(t, resp, http.StatusCreated, "")
	var c requests.Created
	decodeBody(t, resp, &c)
	return c
}

func requestSubmission() requests.SubmitInput {
	encoded := func(n int, b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, n)) }
	envelope, _ := json.Marshal(requests.Envelope{Version: 1, Algorithm: "RSA-OAEP-256+A256GCM", WrappedKey: encoded(256, 1), Nonce: encoded(12, 2)})
	return requests.SubmitInput{Generation: 1, AttemptToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)), Kind: "text", SizeBytes: 5, Envelope: envelope, Ciphertext: encoded(21, 4)}
}

func auth(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}
func assertRequestStatus(t *testing.T, r *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if r.Code != status {
		t.Fatalf("HTTP %d, want %d (%s)", r.Code, status, r.Body.String())
	}
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
	if code != "" {
		var e errorResponse
		decodeBody(t, r, &e)
		if e.Error.Code != code {
			t.Fatalf("code %s, want %s", e.Error.Code, code)
		}
	}
}

func TestRequestHTTPFlowAndRoleSeparation(t *testing.T) {
	router, _, conn := requestFixture(t, 100)
	c := createHTTPRequest(t, router)
	path := "/api/requests/" + c.ID
	in := requestSubmission()
	for _, tc := range []struct {
		method, path, token string
		body                any
	}{
		{http.MethodGet, path, "", nil}, {http.MethodGet, path, c.RetrievalToken, nil},
		{http.MethodGet, path + "/owner", c.SubmissionToken, nil},
		{http.MethodPost, path + "/open", c.SubmissionToken, nil}, {http.MethodPost, path + "/revoke", c.SubmissionToken, nil},
		{http.MethodPost, path + "/submit", c.RetrievalToken, in}, {http.MethodPost, path + "/submit", "bad", in},
		{http.MethodPost, path + "/attempt", c.RetrievalToken, map[string]any{"generation": 1, "attempt_token": in.AttemptToken}},
	} {
		assertRequestStatus(t, performJSON(t, router, tc.method, tc.path, tc.body, auth(tc.token)), 404, "request_unavailable")
	}
	assertRequestStatus(t, performJSON(t, router, http.MethodGet, path, nil, auth(c.SubmissionToken)), 200, "")
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/submit", in, auth(c.SubmissionToken)), 200, "")
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/submit", in, auth(c.SubmissionToken)), 200, "")
	for _, tc := range []struct{ path, token string }{{path, c.SubmissionToken}, {path + "/owner", c.RetrievalToken}} {
		resp := performJSON(t, router, http.MethodGet, tc.path, nil, auth(tc.token))
		assertRequestStatus(t, resp, 200, "")
		for _, sensitive := range []string{"ciphertext", "envelope", c.SubmissionToken, c.RetrievalToken, in.AttemptToken} {
			if strings.Contains(resp.Body.String(), sensitive) {
				t.Fatal("metadata leaked capability or payload")
			}
		}
	}
	attempt := performJSON(t, router, http.MethodPost, path+"/attempt", map[string]any{"generation": 1, "attempt_token": in.AttemptToken}, auth(c.SubmissionToken))
	assertRequestStatus(t, attempt, 200, "")
	var receipt requests.Receipt
	decodeBody(t, attempt, &receipt)
	if receipt.State != "accepted" {
		t.Fatal("lost-response receipt missing")
	}
	// Preview GET/HEAD cannot open or revoke.
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		assertRequestStatus(t, performJSON(t, router, method, path+"/open", nil, auth(c.RetrievalToken)), 405, "")
	}
	opened := performJSON(t, router, http.MethodPost, path+"/open", nil, auth(c.RetrievalToken))
	assertRequestStatus(t, opened, 200, "")
	var payload requests.Payload
	decodeBody(t, opened, &payload)
	if payload.Ciphertext != in.Ciphertext {
		t.Fatal("wrong ciphertext")
	}
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/open", nil, auth(c.RetrievalToken)), 409, "request_conflict")
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/revoke", nil, auth(c.RetrievalToken)), 409, "request_conflict")
	var n int
	if err := conn.QueryRow(`select count(*) from request_payloads`).Scan(&n); err != nil || n != 0 {
		t.Fatal("open retained payload")
	}
}

func TestRequestHTTPRejectsMalformedBodiesAndLargeEndpoints(t *testing.T) {
	router, _, _ := requestFixture(t, 100)
	c := createHTTPRequest(t, router)
	path := "/api/requests/" + c.ID
	in := requestSubmission()
	valid, _ := json.Marshal(in)
	for _, body := range []string{
		"null", "[]", string(valid) + "{}", string(valid[:len(valid)-1]) + `,"plaintext":"must-not-store"}`,
		strings.Replace(string(valid), `"size_bytes":5`, `"size_bytes":1.2`, 1),
		strings.Replace(string(valid), `"size_bytes":5`, `"size_bytes":null`, 1),
		strings.Replace(string(valid), `"size_bytes":5,`, "", 1),
		strings.Replace(string(valid), `"generation":1`, `"Generation":1`, 1),
		`{"generation":1,` + string(valid[1:]),
	} {
		req := httptest.NewRequest(http.MethodPost, path+"/submit", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+c.SubmissionToken)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		assertRequestStatus(t, resp, 400, "invalid_request")
	}
	in.SizeBytes = 1024
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/submit", in, auth(c.SubmissionToken)), 413, "payload_too_large")
	in = requestSubmission()
	in.Ciphertext = strings.Repeat("A", 70000)
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/submit", in, auth(c.SubmissionToken)), 413, "payload_too_large")
	for _, endpoint := range []string{"upload", "finalize", "abandon"} {
		assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/"+endpoint, nil, auth(c.SubmissionToken)), 404, "")
	}
	for _, endpoint := range []string{"open", "revoke"} {
		assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/"+endpoint, map[string]any{}, auth(c.RetrievalToken)), 400, "invalid_request")
	}
	assertRequestStatus(t, performJSON(t, router, http.MethodGet, path+"?token="+c.SubmissionToken, nil), 400, "invalid_request")
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Add("Authorization", "Bearer "+c.SubmissionToken)
	req.Header.Add("Authorization", "Bearer "+c.SubmissionToken)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	assertRequestStatus(t, resp, 404, "request_unavailable")
}

func TestRequestHTTPExpiryRevokeAndStorageFailure(t *testing.T) {
	router, store, conn := requestFixture(t, 100)
	c := createHTTPRequest(t, router)
	path := "/api/requests/" + c.ID
	in := requestSubmission()
	if _, err := conn.Exec(`create trigger fail before insert on request_payloads begin select raise(abort,'private database detail'); end`); err != nil {
		t.Fatal(err)
	}
	resp := performJSON(t, router, http.MethodPost, path+"/submit", in, auth(c.SubmissionToken))
	assertRequestStatus(t, resp, 503, "storage_unavailable")
	if strings.Contains(resp.Body.String(), "private database detail") {
		t.Fatal("storage error leaked")
	}
	if _, err := conn.Exec(`drop trigger fail`); err != nil {
		t.Fatal(err)
	}
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/submit", in, auth(c.SubmissionToken)), 200, "")
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/revoke", nil, auth(c.RetrievalToken)), 200, "")
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/revoke", nil, auth(c.RetrievalToken)), 200, "")
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/open", nil, auth(c.RetrievalToken)), 409, "request_conflict")
	store.SetNowForTest(func() time.Time { return c.ExpiresAt })
	for _, tc := range []struct {
		method, suffix, token string
		body                  any
	}{
		{http.MethodGet, "", c.SubmissionToken, nil}, {http.MethodGet, "/owner", c.RetrievalToken, nil},
		{http.MethodPost, "/submit", c.SubmissionToken, in}, {http.MethodPost, "/open", c.RetrievalToken, nil},
		{http.MethodPost, "/revoke", c.RetrievalToken, nil},
	} {
		assertRequestStatus(t, performJSON(t, router, tc.method, path+tc.suffix, tc.body, auth(tc.token)), 404, "request_unavailable")
	}
}

func TestRequestRateLimitNoStoreAndCORS(t *testing.T) {
	router, _, _ := requestFixture(t, 1)
	c := createHTTPRequest(t, router)
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, "/api/requests", map[string]any{"public_key": requestTestKey}), 429, "rate_limited")
	assertRequestStatus(t, performJSON(t, router, http.MethodGet, "/api/requests/unknown-one", nil, auth(c.SubmissionToken)), 404, "request_unavailable")
	assertRequestStatus(t, performJSON(t, router, http.MethodGet, "/api/requests/unknown-two/owner", nil, auth(c.RetrievalToken)), 429, "rate_limited")
	for _, origin := range []string{"https://web.example", "https://other.example"} {
		resp := performJSON(t, router, http.MethodOptions, "/api/requests/anything/open", nil, map[string]string{"Origin": origin, "Access-Control-Request-Headers": "Authorization"})
		assertRequestStatus(t, resp, 204, "")
		allowed := resp.Header().Get("Access-Control-Allow-Headers")
		if origin == "https://web.example" && !strings.Contains(allowed, "Authorization") {
			t.Fatal("missing auth CORS")
		}
		if origin != "https://web.example" && allowed != "" {
			t.Fatal("cross-origin auth CORS")
		}
	}
}

func TestRequestTelemetryExcludesCapabilitiesKeysAndBodies(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	previous := tracer
	tracer = tp.Tracer("request-http-test")
	t.Cleanup(func() { tracer = previous; _ = tp.Shutdown(context.Background()) })
	var logs bytes.Buffer
	output := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(output) })
	router, _, conn := requestFixture(t, 100)
	c := createHTTPRequest(t, router)
	in := requestSubmission()
	path := "/api/requests/" + c.ID
	assertRequestStatus(t, performJSON(t, router, http.MethodGet, path, nil, auth(c.SubmissionToken)), 200, "")
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/submit", in, auth(c.SubmissionToken)), 200, "")
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/open", nil, auth(c.RetrievalToken)), 200, "")
	metrics := httptest.NewRecorder()
	telemetry.MetricsHandler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	spans, err := json.Marshal(sr.Ended())
	if err != nil {
		t.Fatal(err)
	}
	if ended := sr.Ended(); len(ended) != 4 || ended[3].Name() != "POST /api/requests/{id}/open" {
		t.Fatal("request telemetry check did not record the expected route templates")
	}
	all := logs.String() + string(spans) + metrics.Body.String()
	for _, sensitive := range []string{c.ID, c.SubmissionToken, c.RetrievalToken, requestTestKey, in.AttemptToken, in.Ciphertext, string(in.Envelope), "Bearer "} {
		if strings.Contains(all, sensitive) {
			t.Fatal("request material leaked into telemetry")
		}
	}
	var n int
	if err := conn.QueryRow(`select count(*) from outbox_events`).Scan(&n); err != nil || n != 0 {
		t.Fatal("request material entered outbox")
	}
}
