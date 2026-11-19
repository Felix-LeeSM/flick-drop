package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

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
	// The open transaction already deleted the inline payload, so no job carrying the opened ID is queued.
	due := listOutboxDue(t, fixture.db, fixture.outbox, time.Date(2026, 6, 17, 10, 1, 0, 0, time.UTC), 10)
	if len(due) != 0 {
		t.Fatalf("due outbox events = %+v, want none for an inline open", due)
	}

	var payloadCount int
	if err := fixture.db.QueryRowContext(context.Background(), `select count(*) from secret_payloads where secret_id = $1`, created.ID).Scan(&payloadCount); err != nil {
		t.Fatalf("count payloads after open: %v", err)
	}
	if payloadCount != 0 {
		t.Fatalf("payload count after open = %d, want 0", payloadCount)
	}

	secondGetResp := performJSON(t, router, http.MethodGet, "/api/secrets/"+created.ID, nil)
	if secondGetResp.Code != http.StatusNotFound {
		t.Fatalf("second metadata status = %d, body = %s", secondGetResp.Code, secondGetResp.Body.String())
	}

	secondOpenResp := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{
		"access_proof": base64.StdEncoding.EncodeToString([]byte("proof")),
	})
	if secondOpenResp.Code != http.StatusNotFound {
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
	if secondOpenResp.Code != http.StatusNotFound {
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
	if err := fixture.db.QueryRowContext(context.Background(), `select failed_access_count, consumed_at from secrets where id = $1`, created.ID).Scan(&failedAccessCount, &consumedAt); err != nil {
		t.Fatalf("load failed access count: %v", err)
	}
	if failedAccessCount != 5 {
		t.Fatalf("failed access count = %d, want 5", failedAccessCount)
	}
	if !consumedAt.Valid {
		t.Fatal("consumed_at is null after failed access limit")
	}

	var payloadCount int
	if err := fixture.db.QueryRowContext(context.Background(), `select count(*) from secret_payloads where secret_id = $1`, created.ID).Scan(&payloadCount); err != nil {
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

	// No configured token fails closed.
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

func newTestRouterFixtureWithObjects(t *testing.T, opts Options, objects storage.ObjectStore) testRouterFixture {
	t.Helper()

	ctx := context.Background()
	conn := openHTTPTestDB(t, ctx)
	store, err := secrets.NewStore(conn, secrets.StoreOptions{
		PayloadInlineMaxBytes: 1024,
		MaxObjectBytes:        1 << 20,
		MinTTLSeconds:         300,
		MaxTTLSeconds:         604800,
		Objects:               newTestRegistry(objects),
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

	baseURL := os.Getenv("FLICK_TEST_POSTGRES_URL")
	if baseURL == "" {
		t.Skip("FLICK_TEST_POSTGRES_URL is not set; Postgres-backed httpapi tests run only against the compose/CI service")
	}

	schema := fmt.Sprintf("flick_httpapi_test_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatalf("open postgres admin pool: %v", err)
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("create schema %s", schema)); err != nil {
		_ = admin.Close()
		t.Fatalf("create test schema %s: %v", schema, err)
	}

	scoped, err := url.Parse(baseURL)
	if err != nil {
		_ = admin.Close()
		t.Fatalf("parse FLICK_TEST_POSTGRES_URL: %v", err)
	}
	query := scoped.Query()
	query.Set("search_path", schema)
	scoped.RawQuery = query.Encode()

	conn, err := db.OpenPostgres(ctx, scoped.String())
	if err != nil {
		_ = admin.Close()
		t.Fatalf("open postgres test pool: %v", err)
	}
	if err := db.MigrateAPI(ctx, conn); err != nil {
		_ = conn.Close()
		_ = admin.Close()
		t.Fatalf("migrate api schema %s: %v", schema, err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("drop schema %s cascade", schema))
		_ = admin.Close()
	})

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

// The server checks only the magic line and the length.
func ageBody(n int) []byte {
	return append([]byte(ageMagicLine), bytes.Repeat([]byte{'x'}, n-len(ageMagicLine))...)
}

func largeFilename() *string {
	name := ageField(120)
	return &name
}

func ageField(n int) string {
	return base64.StdEncoding.EncodeToString(ageBody(n))
}

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

// "" in want means the header is absent.
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

	// The decryption key travels in the URL fragment, which the API never receives.
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

	openResp := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{})
	if openResp.Code != http.StatusOK {
		t.Fatalf("open status = %d, body = %s", openResp.Code, openResp.Body.String())
	}
	assertOpenResponse(t, openResp, ageBody(48), map[string]string{
		"Flick-Kind":             secrets.KindText,
		"Flick-Expires-At":       "2026-06-17T10:10:00Z",
		"Flick-Wrapped-Identity": "",
	})

	secondOpen := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{})
	if secondOpen.Code != http.StatusNotFound {
		t.Fatalf("second open status = %d, body = %s", secondOpen.Code, secondOpen.Body.String())
	}
}

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

type streamObjects struct {
	storage.ObjectStore
	body     []byte
	sizeSkew int64
	openErr  error
	opened   []string
	onEOF    func()
	readErr  error
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

func listOutboxDue(t *testing.T, conn *sql.DB, outbox *events.OutboxStore, dueAt time.Time, limit int) []events.OutboxRecord {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin outbox claim: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	records, err := outbox.ClaimDueTx(ctx, tx, dueAt, limit)
	if err != nil {
		t.Fatalf("claim due outbox events: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit outbox claim: %v", err)
	}
	return records
}

func outboxKinds(t *testing.T, conn *sql.DB, outbox *events.OutboxStore) map[string]events.JobEvent {
	t.Helper()

	due := listOutboxDue(t, conn, outbox, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), 100)
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

// The bucket delete is queued only after the body is read to the end.
func TestOpenStreamsS3BodyBeforeEnqueuingObjectDelete(t *testing.T) {
	objects := &streamObjects{body: ageBody(5000)}
	fixture, id := newS3OpenFixture(t, objects)

	var atEOF map[string]events.JobEvent
	objects.onEOF = func() { atEOF = outboxKinds(t, fixture.db, fixture.outbox) }

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
	if len(atEOF) != 0 {
		t.Fatalf("outbox at EOF = %+v, want nothing before the body finished streaming", atEOF)
	}
	after := outboxKinds(t, fixture.db, fixture.outbox)
	job, ok := after[events.KindDeleteOCIObject]
	if !ok || job.ObjectKey != objects.opened[0] || job.Reason != events.ReasonConsumed {
		t.Fatalf("outbox after open = %+v, want consumed object delete for %q", after, objects.opened[0])
	}
}

// The worker deletes only from the backend named in the job.
func TestOpenedObjectDeleteNamesItsBackend(t *testing.T) {
	for _, backend := range []string{testBackend, otherTestBackend} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			conn := openHTTPTestDB(t, ctx)
			objects := &streamObjects{body: ageBody(5000)}
			registry, err := storage.NewRegistry(
				storage.Backend{Name: testBackend, Store: objects, MaxTotalBytes: 1 << 30},
				storage.Backend{Name: otherTestBackend, Store: objects, MaxTotalBytes: 1 << 30},
			)
			if err != nil {
				t.Fatal(err)
			}
			store, err := secrets.NewStore(conn, secrets.StoreOptions{PayloadInlineMaxBytes: 1024, MaxObjectBytes: 1 << 20, MinTTLSeconds: 300, MaxTTLSeconds: 604800, Objects: registry})
			if err != nil {
				t.Fatal(err)
			}
			outbox, err := events.NewOutboxStore(conn, "flick.jobs")
			if err != nil {
				t.Fatal(err)
			}
			router := NewRouter(conn, store, Options{PayloadInlineMaxBytes: 1024, OutboxStore: outbox})
			created, err := store.CreateLarge(ctx, secrets.CreateLargeInput{Kind: secrets.KindFile, EncryptedFilename: largeFilename(), SizeBytes: int64(len(objects.body)), TTLSeconds: 600, MaxViews: 1})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Finalize(ctx, created.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(`update secrets set storage_backend = $1 where id = $2`, backend, created.ID); err != nil {
				t.Fatal(err)
			}

			resp := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/open", map[string]any{})
			if resp.Code != http.StatusOK {
				t.Fatalf("open: %d", resp.Code)
			}

			job, ok := outboxKinds(t, conn, outbox)[events.KindDeleteOCIObject]
			if !ok || job.Backend != backend || job.ObjectKey != "managed/secrets/"+created.ID {
				t.Fatalf("object delete job = %+v, want backend %q", job, backend)
			}
		})
	}
}

func TestOpenS3BodyBrokenMidStreamStillQueuesObjectDelete(t *testing.T) {
	objects := &streamObjects{body: ageBody(5000), readErr: errors.New("connection reset")}
	fixture, id := newS3OpenFixture(t, objects)

	resp := performJSON(t, fixture.router, http.MethodPost, "/api/secrets/"+id+"/open", map[string]any{})
	if resp.Code != http.StatusOK || resp.Body.Len() != 2500 {
		t.Fatalf("broken stream status = %d, body bytes = %d, want 200 and the first half", resp.Code, resp.Body.Len())
	}
	if job, ok := outboxKinds(t, fixture.db, fixture.outbox)[events.KindDeleteOCIObject]; !ok || job.ObjectKey != objects.opened[0] {
		t.Fatalf("object delete after a broken stream = %+v, want one for %q", job, objects.opened[0])
	}
	if again := performJSON(t, fixture.router, http.MethodPost, "/api/secrets/"+id+"/open", map[string]any{}); again.Code != http.StatusNotFound {
		t.Fatalf("reopen after a broken stream status = %d, want 404", again.Code)
	}
}

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
			if kinds := outboxKinds(t, fixture.db, fixture.outbox); len(kinds) != 0 {
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

const (
	testBackend      = "oci"
	otherTestBackend = "r2"
)

func newTestRegistry(objects storage.ObjectStore) *storage.Registry {
	if objects == nil {
		return nil // nil registry = object storage disabled, same as production wiring
	}
	registry, err := storage.NewRegistry(storage.Backend{Name: testBackend, Store: objects})
	if err != nil {
		panic(err)
	}
	return registry
}
