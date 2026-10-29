package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/secrets"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

func TestSecretHTTPFlow(t *testing.T) {
	fixture := newTestRouterFixture(t, Options{
		PayloadInlineMaxBytes: 1024,
		NewJobID: func() (string, error) {
			return "job-consume-1", nil
		},
	})
	router := fixture.router

	createBody := validCreateSecretBody()

	createResp := performJSON(t, router, http.MethodPost, "/api/secrets", createBody)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createResp.Code, createResp.Body.String())
	}
	var created createSecretResponse
	decodeBody(t, createResp, &created)
	if created.ID == "" {
		t.Fatal("expected created id")
	}

	getResp := performJSON(t, router, http.MethodGet, "/api/secrets/"+created.ID, nil)
	if getResp.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, body = %s", getResp.Code, getResp.Body.String())
	}
	metadataBody := getResp.Body.String()
	if strings.Contains(metadataBody, "ciphertext") {
		t.Fatalf("metadata response contains ciphertext: %s", metadataBody)
	}
	var got getSecretMetadataResponse
	decodeBody(t, getResp, &got)
	if got.Access.KDF.Salt != "access-salt" {
		t.Fatalf("access salt = %q, want access-salt", got.Access.KDF.Salt)
	}

	wrongOpenResp := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{
		"access_proof": base64.StdEncoding.EncodeToString([]byte("wrong-proof")),
	})
	if wrongOpenResp.Code != http.StatusForbidden {
		t.Fatalf("wrong open status = %d, body = %s", wrongOpenResp.Code, wrongOpenResp.Body.String())
	}

	openResp := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{
		"access_proof": base64.StdEncoding.EncodeToString([]byte("proof")),
	})
	if openResp.Code != http.StatusOK {
		t.Fatalf("open status = %d, body = %s", openResp.Code, openResp.Body.String())
	}
	assertOpenResponse(t, openResp, ageBody(64), map[string]string{
		"Flick-Kind":               secrets.KindText,
		"Flick-Wrapped-Identity":   createBody["wrapped_identity"].(string),
		"Flick-Expires-At":         "2026-06-17T10:10:00Z",
		"Flick-Content-Type":       "",
		"Flick-Encrypted-Filename": "",
	})
	due, err := fixture.outbox.ListDue(context.Background(), time.Date(2026, 6, 17, 10, 1, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatalf("list due outbox events: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due outbox count = %d, want 1", len(due))
	}
	if due[0].Payload.JobID != "job-consume-1" {
		t.Fatalf("outbox job id = %q, want job-consume-1", due[0].Payload.JobID)
	}
	if due[0].Payload.Kind != events.KindDeleteSecret || due[0].Payload.SecretID != created.ID || due[0].Payload.Reason != events.ReasonConsumed {
		t.Fatalf("outbox payload = %+v, want consumed delete_secret for %q", due[0].Payload, created.ID)
	}
	for _, forbidden := range []string{"payload", "plaintext", "passphrase", "derived_key", "decrypt_key", "ciphertext"} {
		if strings.Contains(due[0].PayloadJSON, forbidden) {
			t.Fatalf("outbox payload contains forbidden value %q: %s", forbidden, due[0].PayloadJSON)
		}
	}

	var payloadCount int
	if err := fixture.db.QueryRowContext(context.Background(), `select count(*) from secret_payloads where secret_id = ?`, created.ID).Scan(&payloadCount); err != nil {
		t.Fatalf("count payloads after consume: %v", err)
	}
	if payloadCount != 1 {
		t.Fatalf("payload count after consume = %d, want 1 until cleanup job runs", payloadCount)
	}

	secondGetResp := performJSON(t, router, http.MethodGet, "/api/secrets/"+created.ID, nil)
	if secondGetResp.Code != http.StatusGone {
		t.Fatalf("second metadata status = %d, body = %s", secondGetResp.Code, secondGetResp.Body.String())
	}

	secondOpenResp := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{
		"access_proof": base64.StdEncoding.EncodeToString([]byte("proof")),
	})
	if secondOpenResp.Code != http.StatusGone {
		t.Fatalf("second open status = %d, body = %s", secondOpenResp.Code, secondOpenResp.Body.String())
	}
}

func TestFileSecretHTTPFlow(t *testing.T) {
	fixture := newTestRouterFixture(t, Options{
		PayloadInlineMaxBytes: 1024,
		NewJobID: func() (string, error) {
			return "job-file-consume-1", nil
		},
	})
	router := fixture.router

	createBody := validCreateSecretBody()
	createBody["kind"] = "file"
	createBody["ciphertext"] = ageField(80)
	createBody["encrypted_filename"] = ageField(120)
	createBody["content_type"] = "text/plain"
	createBody["size_bytes"] = 80

	createResp := performJSON(t, router, http.MethodPost, "/api/secrets", createBody)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create file status = %d, body = %s", createResp.Code, createResp.Body.String())
	}
	var created createSecretResponse
	decodeBody(t, createResp, &created)

	getResp := performJSON(t, router, http.MethodGet, "/api/secrets/"+created.ID, nil)
	if getResp.Code != http.StatusOK {
		t.Fatalf("file metadata status = %d, body = %s", getResp.Code, getResp.Body.String())
	}
	metadataBody := getResp.Body.String()
	for _, forbidden := range []string{"ciphertext", "encrypted_filename", "wrapped_identity", createBody["encrypted_filename"].(string)} {
		if strings.Contains(metadataBody, forbidden) {
			t.Fatalf("file metadata response contains %q: %s", forbidden, metadataBody)
		}
	}
	var metadata getSecretMetadataResponse
	decodeBody(t, getResp, &metadata)
	if metadata.Kind != secrets.KindFile {
		t.Fatalf("metadata kind = %q, want %q", metadata.Kind, secrets.KindFile)
	}

	openResp := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{
		"access_proof": base64.StdEncoding.EncodeToString([]byte("proof")),
	})
	if openResp.Code != http.StatusOK {
		t.Fatalf("file open status = %d, body = %s", openResp.Code, openResp.Body.String())
	}
	assertOpenResponse(t, openResp, ageBody(80), map[string]string{
		"Flick-Kind":               secrets.KindFile,
		"Flick-Content-Type":       "text/plain",
		"Flick-Encrypted-Filename": createBody["encrypted_filename"].(string),
		"Flick-Wrapped-Identity":   createBody["wrapped_identity"].(string),
	})

	secondOpenResp := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{
		"access_proof": base64.StdEncoding.EncodeToString([]byte("proof")),
	})
	if secondOpenResp.Code != http.StatusGone {
		t.Fatalf("second file open status = %d, body = %s", secondOpenResp.Code, secondOpenResp.Body.String())
	}
}

func TestOpenDeletesSecretAfterFiveInvalidProofs(t *testing.T) {
	fixture := newTestRouterFixture(t, Options{
		PayloadInlineMaxBytes: 1024,
		NewJobID: func() (string, error) {
			return "job-consume-after-failures", nil
		},
	})

	createResp := performJSON(t, fixture.router, http.MethodPost, "/api/secrets", validCreateSecretBody())
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createResp.Code, createResp.Body.String())
	}
	var created createSecretResponse
	decodeBody(t, createResp, &created)

	for attempt := 1; attempt <= 5; attempt++ {
		resp := performJSON(t, fixture.router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{
			"access_proof": base64.StdEncoding.EncodeToString([]byte("wrong-proof")),
		})
		if resp.Code != http.StatusForbidden {
			t.Fatalf("wrong open attempt %d status = %d, body = %s", attempt, resp.Code, resp.Body.String())
		}
	}

	var failedAccessCount int
	var consumedAt sql.NullString
	if err := fixture.db.QueryRowContext(context.Background(), `select failed_access_count, consumed_at from secrets where id = ?`, created.ID).Scan(&failedAccessCount, &consumedAt); err != nil {
		t.Fatalf("load failed access count: %v", err)
	}
	if failedAccessCount != 5 {
		t.Fatalf("failed access count = %d, want 5", failedAccessCount)
	}
	if !consumedAt.Valid {
		t.Fatal("consumed_at is null after failed access limit")
	}

	var payloadCount int
	if err := fixture.db.QueryRowContext(context.Background(), `select count(*) from secret_payloads where secret_id = ?`, created.ID).Scan(&payloadCount); err != nil {
		t.Fatalf("count payloads after failed access limit: %v", err)
	}
	if payloadCount != 0 {
		t.Fatalf("payload count after failed access limit = %d, want 0", payloadCount)
	}

	openResp := performJSON(t, fixture.router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{
		"access_proof": base64.StdEncoding.EncodeToString([]byte("proof")),
	})
	if openResp.Code != http.StatusGone {
		t.Fatalf("correct open after failed access limit status = %d, body = %s", openResp.Code, openResp.Body.String())
	}
}

func TestOpenRollsBackWhenOutboxEnqueueFails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	fixture := newTestRouterFixture(t, Options{
		PayloadInlineMaxBytes: 1024,
		NewJobID: func() (string, error) {
			return "job-duplicate", nil
		},
	})

	if _, err := fixture.outbox.Enqueue(ctx, events.JobEvent{
		JobID:       "job-duplicate",
		Kind:        events.KindDeleteSecret,
		SecretID:    "sec-duplicate",
		RequestedAt: now,
	}); err != nil {
		t.Fatalf("seed duplicate outbox event: %v", err)
	}

	createResp := performJSON(t, fixture.router, http.MethodPost, "/api/secrets", validCreateSecretBody())
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createResp.Code, createResp.Body.String())
	}
	var created createSecretResponse
	decodeBody(t, createResp, &created)

	openResp := performJSON(t, fixture.router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{
		"access_proof": base64.StdEncoding.EncodeToString([]byte("proof")),
	})
	if openResp.Code != http.StatusInternalServerError {
		t.Fatalf("open with duplicate outbox id status = %d, body = %s", openResp.Code, openResp.Body.String())
	}

	getResp := performJSON(t, fixture.router, http.MethodGet, "/api/secrets/"+created.ID, nil)
	if getResp.Code != http.StatusOK {
		t.Fatalf("metadata after failed outbox enqueue status = %d, body = %s", getResp.Code, getResp.Body.String())
	}

	due, err := fixture.outbox.ListDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due outbox events: %v", err)
	}
	if len(due) != 1 || due[0].ID != "job-duplicate" {
		t.Fatalf("due outbox records = %+v, want only seeded duplicate event", due)
	}
}

func TestOpenRequiresOutbox(t *testing.T) {
	ctx := context.Background()
	conn := openHTTPTestDB(t, ctx)
	store, err := secrets.NewStore(conn, secrets.StoreOptions{
		PayloadInlineMaxBytes: 1024,
		MinTTLSeconds:         300,
		MaxTTLSeconds:         604800,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	store.SetNowForTest(func() time.Time {
		return time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	})
	router := NewRouter(conn, store, Options{PayloadInlineMaxBytes: 1024})

	createResp := performJSON(t, router, http.MethodPost, "/api/secrets", validCreateSecretBody())
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createResp.Code, createResp.Body.String())
	}
	var created createSecretResponse
	decodeBody(t, createResp, &created)

	openResp := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{
		"access_proof": base64.StdEncoding.EncodeToString([]byte("proof")),
	})
	if openResp.Code != http.StatusInternalServerError {
		t.Fatalf("open without outbox status = %d, body = %s", openResp.Code, openResp.Body.String())
	}

	getResp := performJSON(t, router, http.MethodGet, "/api/secrets/"+created.ID, nil)
	if getResp.Code != http.StatusOK {
		t.Fatalf("metadata after failed open status = %d, body = %s", getResp.Code, getResp.Body.String())
	}
}

func TestCreateRejectsSensitiveFields(t *testing.T) {
	router := newTestRouter(t)

	body := validCreateSecretBody()
	body["passphrase"] = "do-not-send"

	resp := performJSON(t, router, http.MethodPost, "/api/secrets", body)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestCreateRejectsTrailingJSON(t *testing.T) {
	router := newTestRouter(t)

	valid, err := json.Marshal(validCreateSecretBody())
	if err != nil {
		t.Fatal(err)
	}
	body := string(valid) + `{"passphrase":"do-not-send"}`

	req := httptest.NewRequest(http.MethodPost, "/api/secrets", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		responseBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.Code, string(responseBody))
	}
}

func TestMetricsRequiresToken(t *testing.T) {
	router := newTestRouterWithOptions(t, Options{
		PayloadInlineMaxBytes: 1024,
		MetricsToken:          "metrics-token",
	})

	for _, tc := range []struct {
		name string
		auth string
	}{
		{"missing", ""},
		{"wrong scheme", "metrics-token"},
		{"wrong token", "Bearer wrong-token"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("%s: metrics status = %d, want 401, body = %s", tc.name, resp.Code, resp.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer metrics-token")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("correct token metrics status = %d, want 200, body = %s", resp.Code, resp.Body.String())
	}

	// A misconfigured deploy (no token) fails closed: /metrics stays 401 even
	// with a bearer header, so it can never be exposed unauthenticated.
	disabledRouter := newTestRouter(t)
	disabledReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	disabledReq.Header.Set("Authorization", "Bearer anything")
	disabledResp := httptest.NewRecorder()
	disabledRouter.ServeHTTP(disabledResp, disabledReq)
	if disabledResp.Code != http.StatusUnauthorized {
		t.Fatalf("disabled metrics status = %d, want 401", disabledResp.Code)
	}
}

func TestCleanupSecretRequiresInternalToken(t *testing.T) {
	router := newTestRouterWithOptions(t, Options{
		PayloadInlineMaxBytes: 1024,
		InternalToken:         "test-token",
	})

	publicResp := performJSON(t, router, http.MethodPost, "/api/secrets", validCreateSecretBody(), nil)
	if publicResp.Code != http.StatusCreated {
		t.Fatalf("public create status = %d, body = %s", publicResp.Code, publicResp.Body.String())
	}

	for _, token := range []string{"", "wrong-token"} {
		resp := performJSON(t, router, http.MethodPost, "/internal/secrets/s1/cleanup", map[string]any{
			"job_id": "job-1",
			"reason": "expired",
		}, map[string]string{"X-Flick-Internal-Token": token})
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("cleanup status with token %q = %d, body = %s", token, resp.Code, resp.Body.String())
		}
	}

	disabledRouter := newTestRouter(t)
	disabledResp := performJSON(t, disabledRouter, http.MethodPost, "/internal/secrets/s1/cleanup", map[string]any{
		"job_id": "job-1",
		"reason": "expired",
	}, map[string]string{"X-Flick-Internal-Token": "test-token"})
	if disabledResp.Code != http.StatusUnauthorized {
		t.Fatalf("disabled cleanup status = %d, body = %s", disabledResp.Code, disabledResp.Body.String())
	}
}

func TestCleanupSecretDeletesPayload(t *testing.T) {
	router := newTestRouterWithOptions(t, Options{
		PayloadInlineMaxBytes: 1024,
		InternalToken:         "test-token",
	})

	createResp := performJSON(t, router, http.MethodPost, "/api/secrets", validCreateSecretBody(), nil)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createResp.Code, createResp.Body.String())
	}
	var created createSecretResponse
	decodeBody(t, createResp, &created)

	cleanupResp := performJSON(t, router, http.MethodPost, "/internal/secrets/"+created.ID+"/cleanup", map[string]any{
		"job_id": "job-1",
		"reason": "expired",
	}, map[string]string{"X-Flick-Internal-Token": "test-token"})
	if cleanupResp.Code != http.StatusOK {
		t.Fatalf("cleanup status = %d, body = %s", cleanupResp.Code, cleanupResp.Body.String())
	}
	var cleaned cleanupSecretResponse
	decodeBody(t, cleanupResp, &cleaned)
	if cleaned.ID != created.ID || !cleaned.Cleaned {
		t.Fatalf("cleanup response = %+v, want id %q cleaned true", cleaned, created.ID)
	}

	getResp := performJSON(t, router, http.MethodGet, "/api/secrets/"+created.ID, nil, nil)
	if getResp.Code != http.StatusNotFound {
		t.Fatalf("get after cleanup status = %d, body = %s", getResp.Code, getResp.Body.String())
	}

	secondCleanupResp := performJSON(t, router, http.MethodPost, "/internal/secrets/"+created.ID+"/cleanup", map[string]any{
		"job_id": "job-2",
		"reason": "retry",
	}, map[string]string{"X-Flick-Internal-Token": "test-token"})
	if secondCleanupResp.Code != http.StatusOK {
		t.Fatalf("second cleanup status = %d, body = %s", secondCleanupResp.Code, secondCleanupResp.Body.String())
	}
	var secondCleaned cleanupSecretResponse
	decodeBody(t, secondCleanupResp, &secondCleaned)
	if secondCleaned.Cleaned {
		t.Fatalf("second cleanup cleaned = true, want false")
	}
}

func TestCleanupSecretRejectsInvalidMetadata(t *testing.T) {
	router := newTestRouterWithOptions(t, Options{
		PayloadInlineMaxBytes: 1024,
		InternalToken:         "test-token",
	})

	resp := performJSON(t, router, http.MethodPost, "/internal/secrets/s1/cleanup", map[string]any{
		"job_id": "job-1",
		"reason": "passphrase",
	}, map[string]string{"X-Flick-Internal-Token": "test-token"})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid reason status = %d, body = %s", resp.Code, resp.Body.String())
	}

	raw := strings.NewReader(`{"job_id":"job-1","reason":"expired","passphrase":"do-not-send"}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/secrets/s1/cleanup", raw)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flick-Internal-Token", "test-token")
	sensitiveResp := httptest.NewRecorder()
	router.ServeHTTP(sensitiveResp, req)
	if sensitiveResp.Code != http.StatusBadRequest {
		t.Fatalf("sensitive field status = %d, body = %s", sensitiveResp.Code, sensitiveResp.Body.String())
	}
}

func TestReadyzChecksNATS(t *testing.T) {
	notReady := newTestRouterWithOptions(t, Options{
		PayloadInlineMaxBytes: 1024,
		NATSConnected:         func() bool { return false },
	})
	resp := performJSON(t, notReady, http.MethodGet, "/readyz", nil)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz with NATS down status = %d, want 503, body = %s", resp.Code, resp.Body.String())
	}

	ready := newTestRouterWithOptions(t, Options{
		PayloadInlineMaxBytes: 1024,
		NATSConnected:         func() bool { return true },
	})
	readyResp := performJSON(t, ready, http.MethodGet, "/readyz", nil)
	if readyResp.Code != http.StatusOK {
		t.Fatalf("readyz with NATS up status = %d, want 200, body = %s", readyResp.Code, readyResp.Body.String())
	}

	// No NATSConnected wired: readyz stays DB-only and returns 200.
	nilResp := performJSON(t, newTestRouter(t), http.MethodGet, "/readyz", nil)
	if nilResp.Code != http.StatusOK {
		t.Fatalf("readyz without NATS check status = %d, want 200, body = %s", nilResp.Code, nilResp.Body.String())
	}
}

func newTestRouter(t *testing.T) http.Handler {
	t.Helper()

	return newTestRouterFixture(t, Options{PayloadInlineMaxBytes: 1024}).router
}

func newTestRouterWithOptions(t *testing.T, opts Options) http.Handler {
	t.Helper()

	return newTestRouterFixture(t, opts).router
}

type testRouterFixture struct {
	router http.Handler
	db     *sql.DB
	outbox *events.OutboxStore
	store  *secrets.Store
}

func newTestRouterFixture(t *testing.T, opts Options) testRouterFixture {
	t.Helper()

	return newTestRouterFixtureWithObjects(t, opts, nil)
}

// newTestRouterFixtureWithObjects backs the store with objects (nil for an
// inline-only deployment).
func newTestRouterFixtureWithObjects(t *testing.T, opts Options, objects storage.ObjectStore) testRouterFixture {
	t.Helper()

	ctx := context.Background()
	conn := openHTTPTestDB(t, ctx)
	store, err := secrets.NewStore(conn, secrets.StoreOptions{
		PayloadInlineMaxBytes: 1024,
		MaxObjectBytes:        1 << 20,
		MinTTLSeconds:         300,
		MaxTTLSeconds:         604800,
		Objects:               objects,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	outbox, err := events.NewOutboxStore(conn, "flick.jobs")
	if err != nil {
		t.Fatalf("new outbox store: %v", err)
	}
	outbox.SetNowForTest(func() time.Time { return now })
	if opts.OutboxStore == nil {
		opts.OutboxStore = outbox
	}

	return testRouterFixture{
		router: NewRouter(conn, store, opts),
		db:     conn,
		outbox: opts.OutboxStore,
		store:  store,
	}
}

func TestCORSAllowsConfiguredOrigin(t *testing.T) {
	conn := openHTTPTestDB(t, context.Background())
	store, err := secrets.NewStore(conn, secrets.StoreOptions{
		PayloadInlineMaxBytes: 1024,
		MinTTLSeconds:         300,
		MaxTTLSeconds:         604800,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	router := NewRouter(conn, store, Options{
		PayloadInlineMaxBytes: 1024,
		AllowedOrigin:         "http://localhost:5173",
	})

	req := httptest.NewRequest(http.MethodOptions, "/api/secrets", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.Code)
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func openHTTPTestDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()

	conn, err := db.OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	if err := db.MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("migrate api: %v", err)
	}
	return conn
}

func performJSON(t *testing.T, handler http.Handler, method, path string, body any, headers ...map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var requestBody bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&requestBody).Encode(body); err != nil {
			t.Fatalf("encode request body: %v", err)
		}
	}

	req := httptest.NewRequest(method, path, &requestBody)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, set := range headers {
		for key, value := range set {
			req.Header.Set(key, value)
		}
	}

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

const ageMagicLine = "age-encryption.org/v1\n"

// ageBody is an age-shaped file of exactly n bytes: the magic line, then
// padding. The server checks only the magic line and the length.
func ageBody(n int) []byte {
	return append([]byte(ageMagicLine), bytes.Repeat([]byte{'x'}, n-len(ageMagicLine))...)
}

// ageField is ageBody(n) as the base64 the API carries in JSON fields.
func largeFilename() *string {
	name := ageField(120)
	return &name
}

func ageField(n int) string {
	return base64.StdEncoding.EncodeToString(ageBody(n))
}

// validCreateSecretBody is a Model A text secret with a 64-byte age body.
func validCreateSecretBody() map[string]any {
	return map[string]any{
		"kind":       "text",
		"ciphertext": ageField(64),
		"access": map[string]any{
			"kdf": map[string]any{
				"algorithm":       secrets.KDFPBKDF2SHA256,
				"salt":            "access-salt",
				"iterations":      600000,
				"key_length_bits": 256,
			},
			"proof": base64.StdEncoding.EncodeToString([]byte("proof")),
		},
		"wrapped_identity": ageField(200),
		"size_bytes":       64,
		"ttl_seconds":      600,
	}
}

// assertOpenResponse checks an open response: 200, the raw age bytes, the
// transport headers, and each Flick-* header in want ("" means absent).
func assertOpenResponse(t *testing.T, resp *httptest.ResponseRecorder, wantBody []byte, want map[string]string) {
	t.Helper()

	if resp.Code != http.StatusOK {
		t.Fatalf("open status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !bytes.Equal(resp.Body.Bytes(), wantBody) {
		t.Fatalf("open body = %q, want %q", resp.Body.Bytes(), wantBody)
	}
	h := resp.Header()
	for key, value := range map[string]string{
		"Content-Type":   "application/octet-stream",
		"Content-Length": strconv.Itoa(len(wantBody)),
		"Cache-Control":  "no-store",
	} {
		if got := h.Get(key); got != value {
			t.Fatalf("%s = %q, want %q", key, got, value)
		}
	}
	for key, value := range want {
		if value == "" {
			if _, ok := h[key]; ok {
				t.Fatalf("%s present (%q), want absent", key, h.Get(key))
			}
			continue
		}
		if got := h.Get(key); got != value {
			t.Fatalf("%s = %q, want %q", key, got, value)
		}
	}
}

func decodeBody(t *testing.T, resp *httptest.ResponseRecorder, out any) {
	t.Helper()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}

func TestSecretHTTPFlowModelB(t *testing.T) {
	fixture := newTestRouterFixture(t, Options{
		PayloadInlineMaxBytes: 1024,
		NewJobID: func() (string, error) {
			return "job-consume-b", nil
		},
	})
	router := fixture.router

	// Model B create: no kdf, no access. The decryption key travels in the URL
	// fragment, which the API never receives.
	createBody := map[string]any{
		"kind":        "text",
		"ciphertext":  ageField(48),
		"size_bytes":  48,
		"ttl_seconds": 600,
	}

	createResp := performJSON(t, router, http.MethodPost, "/api/secrets", createBody)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createResp.Code, createResp.Body.String())
	}
	var created createSecretResponse
	decodeBody(t, createResp, &created)

	getResp := performJSON(t, router, http.MethodGet, "/api/secrets/"+created.ID, nil)
	if getResp.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, body = %s", getResp.Code, getResp.Body.String())
	}
	if strings.Contains(getResp.Body.String(), "access") {
		t.Fatalf("model B metadata should omit access: %s", getResp.Body.String())
	}

	// Model B opens without a proof; the link is the capability.
	openResp := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{})
	if openResp.Code != http.StatusOK {
		t.Fatalf("open status = %d, body = %s", openResp.Code, openResp.Body.String())
	}
	// Model B has no wrapped identity: the identity is in the URL fragment.
	assertOpenResponse(t, openResp, ageBody(48), map[string]string{
		"Flick-Kind":             secrets.KindText,
		"Flick-Expires-At":       "2026-06-17T10:10:00Z",
		"Flick-Wrapped-Identity": "",
	})

	// One-time open still applies.
	secondOpen := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{})
	if secondOpen.Code != http.StatusGone {
		t.Fatalf("second open status = %d, body = %s", secondOpen.Code, secondOpen.Body.String())
	}
}

// TestCreateValidatesAgeFields covers the age create contract: the body must be
// an age file whose exact length is size_bytes, Model A carries a wrapped
// identity and Model B does not, and only file secrets carry a filename.
func TestCreateValidatesAgeFields(t *testing.T) {
	modelB := func(body map[string]any) {
		delete(body, "access")
		delete(body, "wrapped_identity")
	}
	for name, mutate := range map[string]func(map[string]any){
		"size_bytes is plaintext length": func(b map[string]any) { b["size_bytes"] = 64 - 32 },
		"size_bytes past the body":       func(b map[string]any) { b["size_bytes"] = 65 },
		"body without age magic": func(b map[string]any) {
			b["ciphertext"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, 64))
		},
		"model A without wrapped identity": func(b map[string]any) { delete(b, "wrapped_identity") },
		"model A wrapped identity not age": func(b map[string]any) {
			b["wrapped_identity"] = base64.StdEncoding.EncodeToString([]byte("not an age file"))
		},
		"model A wrapped identity too large": func(b map[string]any) { b["wrapped_identity"] = ageField(1025) },
		"model B with wrapped identity":      func(b map[string]any) { delete(b, "access") },
		"text with encrypted filename":       func(b map[string]any) { b["encrypted_filename"] = ageField(100) },
		"file without encrypted filename": func(b map[string]any) {
			modelB(b)
			b["kind"] = "file"
		},
		"file filename not age": func(b map[string]any) {
			modelB(b)
			b["kind"] = "file"
			b["encrypted_filename"] = base64.StdEncoding.EncodeToString([]byte(`{"nonce":"n","ciphertext":"c"}`))
		},
		"removed nonce field": func(b map[string]any) { b["nonce"] = "nonce" },
	} {
		t.Run(name, func(t *testing.T) {
			body := validCreateSecretBody()
			mutate(body)
			// A router per case: the create limiter would otherwise trip.
			resp := performJSON(t, newTestRouter(t), http.MethodPost, "/api/secrets", body)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", resp.Code, resp.Body.String())
			}
		})
	}

	body := validCreateSecretBody()
	modelB(body)
	body["kind"] = "file"
	body["encrypted_filename"] = ageField(100)
	if resp := performJSON(t, newTestRouter(t), http.MethodPost, "/api/secrets", body); resp.Code != http.StatusCreated {
		t.Fatalf("model B file status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

// streamObjects is a bucket holding one S3 body. Open serves it through a
// reader that calls onEOF when the handler reads past the last byte.
type streamObjects struct {
	storage.ObjectStore
	body     []byte
	sizeSkew int64
	openErr  error
	opened   []string
	onEOF    func()
	// readErr, when set, ends the body with this error after half the bytes,
	// as a bucket connection that drops mid-stream would.
	readErr error
}

func (o *streamObjects) PresignPUT(_ context.Context, key string, _ int64, _ time.Duration) (storage.UploadInstruction, error) {
	return storage.UploadInstruction{URL: "http://bucket.test/" + key}, nil
}

func (o *streamObjects) Head(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{Exists: true, Size: int64(len(o.body))}, nil
}

func (o *streamObjects) Open(_ context.Context, key string) (io.ReadCloser, int64, error) {
	o.opened = append(o.opened, key)
	if o.openErr != nil {
		return nil, 0, o.openErr
	}
	var r io.Reader = bytes.NewReader(o.body)
	if o.readErr != nil {
		r = io.MultiReader(bytes.NewReader(o.body[:len(o.body)/2]), errReader{o.readErr})
	}
	return io.NopCloser(&eofHookReader{r: r, onEOF: o.onEOF}), int64(len(o.body)) + o.sizeSkew, nil
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

type eofHookReader struct {
	r     io.Reader
	onEOF func()
	fired bool
}

func (e *eofHookReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF && !e.fired && e.onEOF != nil {
		e.fired = true
		e.onEOF()
	}
	return n, err
}

// outboxKinds lists the kinds of every outbox job, in no particular order.
func outboxKinds(t *testing.T, outbox *events.OutboxStore) map[string]events.JobEvent {
	t.Helper()

	due, err := outbox.ListDue(context.Background(), time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), 100)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	kinds := map[string]events.JobEvent{}
	for _, record := range due {
		kinds[record.Payload.Kind] = record.Payload
	}
	return kinds
}

func newS3OpenFixture(t *testing.T, objects *streamObjects) (testRouterFixture, string) {
	t.Helper()

	jobs := 0
	fixture := newTestRouterFixtureWithObjects(t, Options{
		PayloadInlineMaxBytes: 1024,
		NewJobID: func() (string, error) {
			jobs++
			return "job-s3-" + strconv.Itoa(jobs), nil
		},
	}, objects)
	ctx := context.Background()
	created, err := fixture.store.CreateLarge(ctx, secrets.CreateLargeInput{
		Kind:              secrets.KindFile,
		EncryptedFilename: largeFilename(),
		SizeBytes:         int64(len(objects.body)),
		TTLSeconds:        600,
		MaxViews:          1,
	})
	if err != nil {
		t.Fatalf("create large: %v", err)
	}
	if err := fixture.store.Finalize(ctx, created.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	return fixture, created.ID
}

// TestOpenStreamsS3BodyBeforeEnqueuingObjectDelete proves an S3 body larger
// than the inline cap streams from the object store's Open, and that the
// bucket delete is queued only once the body has been read to the end, while
// the DB cleanup job is already committed with the open.
func TestOpenStreamsS3BodyBeforeEnqueuingObjectDelete(t *testing.T) {
	objects := &streamObjects{body: ageBody(5000)}
	fixture, id := newS3OpenFixture(t, objects)

	var atEOF map[string]events.JobEvent
	objects.onEOF = func() { atEOF = outboxKinds(t, fixture.outbox) }

	resp := performJSON(t, fixture.router, http.MethodPost, "/api/secrets/"+id+"/open", map[string]any{})
	assertOpenResponse(t, resp, objects.body, map[string]string{
		"Flick-Kind":             secrets.KindFile,
		"Flick-Wrapped-Identity": "",
	})
	if len(objects.opened) != 1 {
		t.Fatalf("object opens = %v, want one", objects.opened)
	}
	if atEOF == nil {
		t.Fatal("body was not read to EOF")
	}
	if _, ok := atEOF[events.KindDeleteSecret]; !ok {
		t.Fatalf("outbox at EOF = %+v, want the DB cleanup job from the open transaction", atEOF)
	}
	if _, ok := atEOF[events.KindDeleteOCIObject]; ok {
		t.Fatalf("object delete was queued before the body finished streaming: %+v", atEOF)
	}
	after := outboxKinds(t, fixture.outbox)
	job, ok := after[events.KindDeleteOCIObject]
	if !ok || job.ObjectKey != objects.opened[0] || job.Reason != events.ReasonConsumed {
		t.Fatalf("outbox after open = %+v, want consumed object delete for %q", after, objects.opened[0])
	}
}

// TestOpenS3BodyBrokenMidStreamStillQueuesObjectDelete covers a bucket read that
// fails after the 200 status: the secret is consumed (lost by design), the
// recipient gets a short body, and the object still gets its delete job.
func TestOpenS3BodyBrokenMidStreamStillQueuesObjectDelete(t *testing.T) {
	objects := &streamObjects{body: ageBody(5000), readErr: errors.New("connection reset")}
	fixture, id := newS3OpenFixture(t, objects)

	resp := performJSON(t, fixture.router, http.MethodPost, "/api/secrets/"+id+"/open", map[string]any{})
	if resp.Code != http.StatusOK || resp.Body.Len() != 2500 {
		t.Fatalf("broken stream status = %d, body bytes = %d, want 200 and the first half", resp.Code, resp.Body.Len())
	}
	if job, ok := outboxKinds(t, fixture.outbox)[events.KindDeleteOCIObject]; !ok || job.ObjectKey != objects.opened[0] {
		t.Fatalf("object delete after a broken stream = %+v, want one for %q", job, objects.opened[0])
	}
	if again := performJSON(t, fixture.router, http.MethodPost, "/api/secrets/"+id+"/open", map[string]any{}); again.Code != http.StatusGone {
		t.Fatalf("reopen after a broken stream status = %d, want 410", again.Code)
	}
}

// TestOpenS3BodyFailureLeavesSecretOpenable proves a bucket that cannot start
// the read, or reports a length other than size_bytes, refuses the open
// without consuming the secret or queueing any cleanup.
func TestOpenS3BodyFailureLeavesSecretOpenable(t *testing.T) {
	for name, breakOpen := range map[string]func(*streamObjects){
		"open fails":      func(o *streamObjects) { o.openErr = errors.New("bucket unavailable") },
		"length mismatch": func(o *streamObjects) { o.sizeSkew = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			objects := &streamObjects{body: ageBody(5000)}
			fixture, id := newS3OpenFixture(t, objects)
			breakOpen(objects)

			resp := performJSON(t, fixture.router, http.MethodPost, "/api/secrets/"+id+"/open", map[string]any{})
			if resp.Code != http.StatusInternalServerError {
				t.Fatalf("broken open status = %d, body = %s", resp.Code, resp.Body.String())
			}
			if kinds := outboxKinds(t, fixture.outbox); len(kinds) != 0 {
				t.Fatalf("outbox after refused open = %+v, want empty", kinds)
			}
			if got := performJSON(t, fixture.router, http.MethodGet, "/api/secrets/"+id, nil); got.Code != http.StatusOK {
				t.Fatalf("metadata after refused open status = %d, want 200 (not consumed)", got.Code)
			}

			objects.openErr, objects.sizeSkew = nil, 0
			retry := performJSON(t, fixture.router, http.MethodPost, "/api/secrets/"+id+"/open", map[string]any{})
			assertOpenResponse(t, retry, objects.body, map[string]string{"Flick-Kind": secrets.KindFile})
		})
	}
}
