package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/secrets"
)

func TestSecretHTTPFlow(t *testing.T) {
	fixture := newTestRouterFixture(t, Options{
		PayloadInlineMaxBytes: 1024,
		NewJobID: func() (string, error) {
			return "job-consume-1", nil
		},
	})
	router := fixture.router

	createBody := map[string]any{
		"kind":       "text",
		"ciphertext": base64.StdEncoding.EncodeToString([]byte("ciphertext")),
		"nonce":      "nonce",
		"kdf": map[string]any{
			"algorithm":       secrets.KDFPBKDF2SHA256,
			"salt":            "salt",
			"iterations":      600000,
			"key_length_bits": 256,
		},
		"access": map[string]any{
			"kdf": map[string]any{
				"algorithm":       secrets.KDFPBKDF2SHA256,
				"salt":            "access-salt",
				"iterations":      600000,
				"key_length_bits": 256,
			},
			"proof": base64.StdEncoding.EncodeToString([]byte("proof")),
		},
		"size_bytes":  10,
		"ttl_seconds": 600,
	}

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
	var opened openSecretResponse
	decodeBody(t, openResp, &opened)
	if opened.Ciphertext != createBody["ciphertext"] {
		t.Fatalf("opened ciphertext = %q, want %q", opened.Ciphertext, createBody["ciphertext"])
	}
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
	createBody["ciphertext"] = base64.StdEncoding.EncodeToString([]byte("encrypted-file-bytes"))
	createBody["encrypted_filename"] = `{"nonce":"filename-nonce","ciphertext":"filename-ciphertext"}`
	createBody["content_type"] = "text/plain"
	createBody["size_bytes"] = 20

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
	for _, forbidden := range []string{"ciphertext", "encrypted_filename", "filename-ciphertext"} {
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
	var opened openSecretResponse
	decodeBody(t, openResp, &opened)
	if opened.Kind != secrets.KindFile {
		t.Fatalf("opened kind = %q, want %q", opened.Kind, secrets.KindFile)
	}
	if opened.Ciphertext != createBody["ciphertext"] {
		t.Fatalf("opened ciphertext = %q, want %q", opened.Ciphertext, createBody["ciphertext"])
	}
	if opened.EncryptedFilename == nil || *opened.EncryptedFilename != createBody["encrypted_filename"] {
		t.Fatalf("opened encrypted filename = %v, want %q", opened.EncryptedFilename, createBody["encrypted_filename"])
	}
	if opened.ContentType == nil || *opened.ContentType != "text/plain" {
		t.Fatalf("opened content type = %v, want text/plain", opened.ContentType)
	}

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

	body := map[string]any{
		"kind":       "text",
		"ciphertext": base64.StdEncoding.EncodeToString([]byte("ciphertext")),
		"nonce":      "nonce",
		"passphrase": "do-not-send",
		"kdf": map[string]any{
			"algorithm":       secrets.KDFPBKDF2SHA256,
			"salt":            "salt",
			"iterations":      600000,
			"key_length_bits": 256,
		},
		"size_bytes":  10,
		"ttl_seconds": 600,
	}

	resp := performJSON(t, router, http.MethodPost, "/api/secrets", body)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestCreateRejectsTrailingJSON(t *testing.T) {
	router := newTestRouter(t)

	body := `{
		"kind": "text",
		"ciphertext": "Y2lwaGVydGV4dA==",
		"nonce": "nonce",
		"kdf": {
			"algorithm": "PBKDF2-SHA-256",
			"salt": "salt",
			"iterations": 600000,
			"key_length_bits": 256
		},
		"size_bytes": 10,
		"ttl_seconds": 600
	}{"passphrase":"do-not-send"}`

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
}

func newTestRouterFixture(t *testing.T, opts Options) testRouterFixture {
	t.Helper()

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

func validCreateSecretBody() map[string]any {
	return map[string]any{
		"kind":       "text",
		"ciphertext": base64.StdEncoding.EncodeToString([]byte("ciphertext")),
		"nonce":      "nonce",
		"kdf": map[string]any{
			"algorithm":       secrets.KDFPBKDF2SHA256,
			"salt":            "salt",
			"iterations":      600000,
			"key_length_bits": 256,
		},
		"access": map[string]any{
			"kdf": map[string]any{
				"algorithm":       secrets.KDFPBKDF2SHA256,
				"salt":            "access-salt",
				"iterations":      600000,
				"key_length_bits": 256,
			},
			"proof": base64.StdEncoding.EncodeToString([]byte("proof")),
		},
		"size_bytes":  10,
		"ttl_seconds": 600,
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
		"ciphertext":  base64.StdEncoding.EncodeToString([]byte("ciphertext")),
		"nonce":       "nonce",
		"size_bytes":  10,
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
	var opened openSecretResponse
	decodeBody(t, openResp, &opened)
	if opened.Ciphertext != createBody["ciphertext"] {
		t.Fatalf("opened ciphertext = %q, want %q", opened.Ciphertext, createBody["ciphertext"])
	}

	// One-time open still applies.
	secondOpen := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{})
	if secondOpen.Code != http.StatusGone {
		t.Fatalf("second open status = %d, body = %s", secondOpen.Code, secondOpen.Body.String())
	}
}
