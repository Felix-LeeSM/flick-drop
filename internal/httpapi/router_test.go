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
	"testing"
	"time"

	"github.com/Felix-LeeSM/burn-links/internal/db"
	"github.com/Felix-LeeSM/burn-links/internal/secrets"
)

func TestSecretHTTPFlow(t *testing.T) {
	router := newTestRouter(t)

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
		t.Fatalf("get status = %d, body = %s", getResp.Code, getResp.Body.String())
	}
	var got getSecretResponse
	decodeBody(t, getResp, &got)
	if got.Ciphertext != createBody["ciphertext"] {
		t.Fatalf("ciphertext = %q, want %q", got.Ciphertext, createBody["ciphertext"])
	}

	consumeResp := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/consume", nil)
	if consumeResp.Code != http.StatusAccepted {
		t.Fatalf("consume status = %d, body = %s", consumeResp.Code, consumeResp.Body.String())
	}

	secondGetResp := performJSON(t, router, http.MethodGet, "/api/secrets/"+created.ID, nil)
	if secondGetResp.Code != http.StatusGone {
		t.Fatalf("second get status = %d, body = %s", secondGetResp.Code, secondGetResp.Body.String())
	}

	secondConsumeResp := performJSON(t, router, http.MethodPost, "/api/secrets/"+created.ID+"/consume", nil)
	if secondConsumeResp.Code != http.StatusConflict {
		t.Fatalf("second consume status = %d, body = %s", secondConsumeResp.Code, secondConsumeResp.Body.String())
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

func newTestRouter(t *testing.T) http.Handler {
	t.Helper()

	ctx := context.Background()
	conn := openHTTPTestDB(t, ctx)
	store, err := secrets.NewStore(conn, secrets.StoreOptions{
		PayloadInlineMaxBytes: 1024,
		AllowedTTLSeconds:     []int{600, 3600, 86400},
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	return NewRouter(conn, store, Options{PayloadInlineMaxBytes: 1024})
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

func performJSON(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
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

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func decodeBody(t *testing.T, resp *httptest.ResponseRecorder, out any) {
	t.Helper()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}
