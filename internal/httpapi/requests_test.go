package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/Felix-LeeSM/flick-drop/internal/requests"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var requestKeyOnce sync.Once
var requestTestKey string

func requestFixture(t *testing.T, rate int) (http.Handler, *requests.Store, *sql.DB) {
	return requestFixtureWithOptions(t, rate, Options{})
}

func requestFixtureWithOptions(t *testing.T, rate int, opts Options) (http.Handler, *requests.Store, *sql.DB) {
	t.Helper()
	conn := openHTTPTestDB(t, context.Background())
	store, err := requests.NewStore(conn, requests.Options{PayloadInlineMaxBytes: 1024, MaxObjectBytes: 900, MinTTLSeconds: 300, DefaultTTLSeconds: 600, MaxTTLSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	opts.RequestStore = store
	if opts.PayloadInlineMaxBytes <= 0 {
		opts.PayloadInlineMaxBytes = 1024
	}
	opts.OpenRatePerMinute = rate
	opts.CreateRatePerMinute = rate
	if opts.AllowedOrigin == "" {
		opts.AllowedOrigin = "https://web.example"
	}
	return NewRouter(conn, nil, opts), store, conn
}

func createHTTPRequest(t *testing.T, router http.Handler) requests.Created {
	t.Helper()
	requestKeyOnce.Do(func() {
		identity, err := age.GenerateHybridIdentity()
		if err != nil {
			t.Fatal(err)
		}
		requestTestKey = identity.Recipient().String()
	})
	resp := performJSON(t, router, http.MethodPost, "/api/requests", map[string]any{"public_key": requestTestKey})
	assertRequestStatus(t, resp, http.StatusCreated, "")
	var c requests.Created
	decodeBody(t, resp, &c)
	return c
}

// requestAgeBody is age-shaped: the API checks only the header magic.
func requestAgeBody(size int, b byte) []byte {
	return append([]byte(requests.AgeMagic), bytes.Repeat([]byte{b}, size-len(requests.AgeMagic))...)
}

func requestSubmission() requests.SubmitInput {
	envelope, _ := json.Marshal(requests.Envelope{Version: 2})
	return requests.SubmitInput{Generation: 1, AttemptToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)), Kind: "text", SizeBytes: 60, Envelope: envelope, Ciphertext: base64.StdEncoding.EncodeToString(requestAgeBody(60, 4))}
}

// assertOpenedBody checks an open response: the raw age body with its
// metadata in headers.
func assertOpenedBody(t *testing.T, r *httptest.ResponseRecorder, kind string, want []byte) {
	t.Helper()
	body, _ := io.ReadAll(r.Body)
	h := r.Header()
	if h.Get("Content-Type") != "application/octet-stream" || h.Get("Content-Length") != fmt.Sprint(len(want)) || h.Get("Flick-Kind") != kind || !bytes.Equal(body, want) {
		t.Fatalf("opened body or headers: %v %q", h, body)
	}
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
	want, _ := base64.StdEncoding.DecodeString(in.Ciphertext)
	assertOpenedBody(t, opened, "text", want)
	if opened.Header().Get("Flick-Encrypted-Filename") != "" {
		t.Fatal("text open carried a filename")
	}
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/open", nil, auth(c.RetrievalToken)), 409, "request_conflict")
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/revoke", nil, auth(c.RetrievalToken)), 409, "request_conflict")
	var n int
	if err := conn.QueryRow(`select count(*) from request_payloads`).Scan(&n); err != nil || n != 0 {
		t.Fatal("open retained payload")
	}
}

func TestRequestHTTPRejectsMalformedBodies(t *testing.T) {
	router, _, _ := requestFixture(t, 100)
	c := createHTTPRequest(t, router)
	path := "/api/requests/" + c.ID
	in := requestSubmission()
	valid, _ := json.Marshal(in)
	for _, body := range []string{
		"null", "[]", string(valid) + "{}", string(valid[:len(valid)-1]) + `,"plaintext":"must-not-store"}`,
		strings.Replace(string(valid), `"size_bytes":60`, `"size_bytes":1.2`, 1),
		strings.Replace(string(valid), `"size_bytes":60`, `"size_bytes":null`, 1),
		strings.Replace(string(valid), `"size_bytes":60,`, "", 1),
		strings.Replace(string(valid), `"generation":1`, `"Generation":1`, 1),
		`{"generation":1,` + string(valid[1:]),
	} {
		req := httptest.NewRequest(http.MethodPost, path+"/submit", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+c.SubmissionToken)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		assertRequestStatus(t, resp, 400, "invalid_request")
	}
	in.SizeBytes = 1025
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/submit", in, auth(c.SubmissionToken)), 413, "payload_too_large")
	in = requestSubmission()
	in.Ciphertext = strings.Repeat("A", 70000)
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/submit", in, auth(c.SubmissionToken)), 413, "payload_too_large")
	for _, endpoint := range []string{"upload", "finalize", "abandon"} {
		assertRequestStatus(t, performJSON(t, router, http.MethodPost, path+"/"+endpoint, nil, auth(c.SubmissionToken)), 400, "invalid_request")
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

// injectInsertFailure aborts request_payloads inserts with a raised plpgsql
// exception — the Postgres analogue of the SQLite raise(abort) trigger. The
// statement fails inside the submit transaction, so it must roll back with it.
// The returned func drops the trigger mid-test; cleanup also calls it.
func injectInsertFailure(t *testing.T, conn *sql.DB) func() {
	t.Helper()
	if _, err := conn.Exec(`create function flick_test_fail_submit_fn() returns trigger language plpgsql as $fn$
		begin
			raise exception 'injected storage failure';
		end;
		$fn$`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`create trigger flick_test_fail_submit before insert on request_payloads
		for each row execute function flick_test_fail_submit_fn()`); err != nil {
		t.Fatal(err)
	}
	drop := func() {
		_, _ = conn.Exec(`drop trigger if exists flick_test_fail_submit on request_payloads`)
		_, _ = conn.Exec(`drop function if exists flick_test_fail_submit_fn`)
	}
	t.Cleanup(drop)
	return drop
}

func TestRequestHTTPExpiryRevokeAndStorageFailure(t *testing.T) {
	router, store, conn := requestFixture(t, 100)
	c := createHTTPRequest(t, router)
	path := "/api/requests/" + c.ID
	in := requestSubmission()
	dropTrigger := injectInsertFailure(t, conn)
	resp := performJSON(t, router, http.MethodPost, path+"/submit", in, auth(c.SubmissionToken))
	assertRequestStatus(t, resp, 503, "storage_unavailable")
	if strings.Contains(resp.Body.String(), "injected storage failure") {
		t.Fatal("storage error leaked")
	}
	dropTrigger()
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
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	var logs bytes.Buffer
	output := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(output) })
	router, _, conn := requestFixtureWithOptions(t, 100, Options{TracerProvider: tp})
	telemetryNote := base64.StdEncoding.EncodeToString(requestAgeBody(72, 11))
	cResp := performJSON(t, router, http.MethodPost, "/api/requests", map[string]any{
		"public_key":     requestTestKey,
		"encrypted_note": telemetryNote,
	})
	assertRequestStatus(t, cResp, http.StatusCreated, "")
	var c requests.Created
	decodeBody(t, cResp, &c)
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
	for _, sensitive := range []string{c.ID, c.SubmissionToken, c.RetrievalToken, requestTestKey, telemetryNote, in.AttemptToken, in.Ciphertext, string(in.Envelope), "Bearer "} {
		if strings.Contains(all, sensitive) {
			t.Fatal("request material leaked into telemetry")
		}
	}
	var n int
	if err := conn.QueryRow(`select count(*) from outbox_events`).Scan(&n); err != nil || n != 0 {
		t.Fatal("request material entered outbox")
	}
}

func TestRequestEncryptedNoteHTTPRoundTripAndWipe(t *testing.T) {
	router, _, conn := requestFixture(t, 100)
	_ = createHTTPRequest(t, router)
	noteCiphertext := base64.StdEncoding.EncodeToString(requestAgeBody(80, 7))

	// Invalid encrypted notes are rejected before persistence.
	for _, bad := range []any{"", base64.StdEncoding.EncodeToString([]byte("not-age")), nil} {
		resp := performJSON(t, router, http.MethodPost, "/api/requests", map[string]any{
			"public_key":     requestTestKey,
			"encrypted_note": bad,
		})
		assertRequestStatus(t, resp, 400, "invalid_request")
	}

	resp := performJSON(t, router, http.MethodPost, "/api/requests", map[string]any{
		"public_key":     requestTestKey,
		"encrypted_note": noteCiphertext,
	})
	assertRequestStatus(t, resp, http.StatusCreated, "")
	var created requests.Created
	decodeBody(t, resp, &created)

	// Submission instructions include encrypted_note; owner status omits it.
	instResp := performJSON(t, router, http.MethodGet, "/api/requests/"+created.ID, nil, auth(created.SubmissionToken))
	assertRequestStatus(t, instResp, http.StatusOK, "")
	var inst requests.Instructions
	decodeBody(t, instResp, &inst)
	if inst.EncryptedNote != noteCiphertext {
		t.Fatalf("instructions encrypted_note = %q, want %q", inst.EncryptedNote, noteCiphertext)
	}
	ownerResp := performJSON(t, router, http.MethodGet, "/api/requests/"+created.ID+"/owner", nil, auth(created.RetrievalToken))
	assertRequestStatus(t, ownerResp, http.StatusOK, "")
	if strings.Contains(ownerResp.Body.String(), "encrypted_note") {
		t.Fatalf("owner status exposed encrypted_note: %s", ownerResp.Body.String())
	}

	// Revoking clears encrypted_note from SQLite.
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, "/api/requests/"+created.ID+"/revoke", nil, auth(created.RetrievalToken)), 200, "")
	var stored sql.NullString
	if err := conn.QueryRow(`select encrypted_note from requests where id = $1`, created.ID).Scan(&stored); err != nil || stored.Valid {
		t.Fatalf("encrypted_note survived revoke: %+v err=%v", stored, err)
	}

	// Submitted GET hides encrypted_note; HTTP open clears encrypted_note in SQLite.
	openResp := performJSON(t, router, http.MethodPost, "/api/requests", map[string]any{
		"public_key":     requestTestKey,
		"encrypted_note": noteCiphertext,
	})
	assertRequestStatus(t, openResp, http.StatusCreated, "")
	var openCreated requests.Created
	decodeBody(t, openResp, &openCreated)
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, "/api/requests/"+openCreated.ID+"/submit", requestSubmission(), auth(openCreated.SubmissionToken)), 200, "")
	submittedInstResp := performJSON(t, router, http.MethodGet, "/api/requests/"+openCreated.ID, nil, auth(openCreated.SubmissionToken))
	assertRequestStatus(t, submittedInstResp, http.StatusOK, "")
	if strings.Contains(submittedInstResp.Body.String(), "encrypted_note") {
		t.Fatalf("submitted GET exposed encrypted_note: %s", submittedInstResp.Body.String())
	}
	assertRequestStatus(t, performJSON(t, router, http.MethodPost, "/api/requests/"+openCreated.ID+"/open", nil, auth(openCreated.RetrievalToken)), 200, "")
	if err := conn.QueryRow(`select encrypted_note from requests where id = $1`, openCreated.ID).Scan(&stored); err != nil || stored.Valid {
		t.Fatalf("encrypted_note survived HTTP open: %+v err=%v", stored, err)
	}

	// Bodies above 16 KiB cap on POST /api/requests return 413 payload_too_large.
	hugeResp := performJSON(t, router, http.MethodPost, "/api/requests", map[string]any{
		"public_key":     requestTestKey,
		"encrypted_note": strings.Repeat("A", 16385),
	})
	assertRequestStatus(t, hugeResp, http.StatusRequestEntityTooLarge, "payload_too_large")
}
