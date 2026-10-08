package httpapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/Felix-LeeSM/flick-drop/internal/secrets"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

func TestManagementHTTPAuthorizationAndOutcomes(t *testing.T) {
	for _, outcome := range []string{"opened", "locked"} {
		t.Run(outcome, func(t *testing.T) {
			f := newTestRouterFixture(t, Options{OpenRatePerMinute: 100, InternalToken: "test-token"})
			resp := performJSON(t, f.router, http.MethodPost, "/api/secrets", validCreateSecretBody())
			if resp.Code != http.StatusCreated || resp.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("create: %d", resp.Code)
			}
			var created createSecretResponse
			decodeBody(t, resp, &created)
			if created.ManagementToken == "" || created.ManagementExpiresAt != created.ExpiresAt {
				t.Fatal("missing management response")
			}
			path := "/api/secrets/" + created.ID
			headers := map[string]string{"Authorization": "Bearer " + created.ManagementToken}
			var rejection string
			for _, authorization := range []string{"", "Basic " + created.ManagementToken, "Bearer bad", "Bearer " + created.ManagementToken + "="} {
				resp := performJSON(t, f.router, http.MethodGet, path+"/management", nil, map[string]string{"Authorization": authorization})
				if resp.Code != http.StatusNotFound || resp.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("invalid authorization: %d", resp.Code)
				}
				if rejection != "" && resp.Body.String() != rejection {
					t.Fatal("authorization reason leaked")
				}
				rejection = resp.Body.String()
			}
			unknown := performJSON(t, f.router, http.MethodGet, "/api/secrets/missing/management", nil, headers)
			if unknown.Body.String() != rejection {
				t.Fatal("unknown record distinguished")
			}
			resp = performJSON(t, f.router, http.MethodGet, path+"/management", nil, headers)
			var snapshot map[string]any
			decodeBody(t, resp, &snapshot)
			if resp.Code != http.StatusOK || len(snapshot) != 5 || snapshot["status"] != "active" || snapshot["can_cancel"] != true {
				t.Fatalf("snapshot: %v", snapshot)
			}
			public := performJSON(t, f.router, http.MethodGet, path, nil)
			if strings.Contains(public.Body.String(), "management") || strings.Contains(public.Body.String(), "status") {
				t.Fatal("public metadata gained management fields")
			}
			var failed int
			if err := f.db.QueryRow(`select failed_access_count from secrets where id = ?`, created.ID).Scan(&failed); err != nil || failed != 0 {
				t.Fatal("status request changed proof attempts", err)
			}
			rawToken, err := base64.RawURLEncoding.DecodeString(created.ManagementToken)
			if err != nil {
				t.Fatal(err)
			}
			misused := performJSON(t, f.router, http.MethodPost, path+"/open", map[string]string{"access_proof": base64.StdEncoding.EncodeToString(rawToken)})
			if misused.Code != http.StatusForbidden {
				t.Fatal("management token replaced the recipient proof", misused.Code)
			}
			attempts, proof := 1, "proof"
			if outcome == "locked" {
				attempts, proof = 4, "wrong" // The management-token misuse was the first invalid proof.
			}
			for i := 0; i < attempts; i++ {
				resp = performJSON(t, f.router, http.MethodPost, path+"/open", map[string]any{"access_proof": base64.StdEncoding.EncodeToString([]byte(proof))})
				want := http.StatusOK
				if outcome == "locked" {
					want = http.StatusForbidden
				}
				if resp.Code != want {
					t.Fatalf("open: %d", resp.Code)
				}
				if strings.Contains(resp.Body.String(), "management") {
					t.Fatal("open leaked capability")
				}
			}
			resp = performJSON(t, f.router, http.MethodPost, "/internal/secrets/"+created.ID+"/cleanup", map[string]string{"job_id": "cleanup-test", "reason": "consumed"}, map[string]string{"X-Flick-Internal-Token": "test-token"})
			if resp.Code != http.StatusOK {
				t.Fatalf("cleanup: %d", resp.Code)
			}
			resp = performJSON(t, f.router, http.MethodGet, path+"/management", nil, headers)
			decodeBody(t, resp, &snapshot)
			if snapshot["status"] != outcome || snapshot["can_cancel"] != false {
				t.Fatalf("terminal snapshot: %v", snapshot)
			}
			if _, err := f.db.Exec(`update secret_management set expires_at = '2000-01-01T00:00:00.000000000Z'`); err != nil {
				t.Fatal(err)
			}
			resp = performJSON(t, f.router, http.MethodGet, path+"/management", nil, headers)
			if resp.Body.String() != rejection {
				t.Fatal("expired capability distinguished")
			}
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			resp = performJSON(t, f.router, http.MethodGet, path+"/management", nil, headers)
			if resp.Code != http.StatusServiceUnavailable || strings.Contains(resp.Body.String(), created.ManagementToken) {
				t.Fatal("database failure misreported")
			}
		})
	}
}

func TestManagementRateAndCORS(t *testing.T) {
	f := newTestRouterFixture(t, Options{OpenRatePerMinute: 1, AllowedOrigin: "https://web.example"})
	resp := performJSON(t, f.router, http.MethodPost, "/api/secrets", validCreateSecretBody())
	var created createSecretResponse
	decodeBody(t, resp, &created)
	path := "/api/secrets/" + created.ID
	for _, origin := range []string{"https://web.example", "https://other.example"} {
		resp := performJSON(t, f.router, http.MethodOptions, path+"/management", nil, map[string]string{"Origin": origin, "Access-Control-Request-Headers": "Authorization"})
		allowed := resp.Header().Get("Access-Control-Allow-Headers")
		if (origin == "https://web.example") != strings.Contains(allowed, "Authorization") {
			t.Fatal("incorrect authorization CORS", allowed)
		}
	}
	for i, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
		resp := performJSON(t, f.router, http.MethodGet, path+"/management", nil, map[string]string{"Authorization": "Bearer " + created.ManagementToken})
		if resp.Code != want || resp.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("management request %d: %d", i, resp.Code)
		}
	}
	resp = performJSON(t, f.router, http.MethodPost, path+"/open", map[string]string{"access_proof": base64.StdEncoding.EncodeToString([]byte("proof"))})
	if resp.Code != http.StatusOK {
		t.Fatal("management depleted recipient limiter", resp.Code)
	}

}

func TestLargeCreateReturnsManagementCapability(t *testing.T) {
	ctx := context.Background()
	conn := openHTTPTestDB(t, ctx)
	objects, err := storage.New(storage.Config{Enabled: true, Endpoint: "http://localhost:9000", Region: "us-east-1", Bucket: "test-bucket", AccessKeyID: "test-key", SecretAccessKey: "test-secret", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	store, err := secrets.NewStore(conn, secrets.StoreOptions{PayloadInlineMaxBytes: 1024, MaxObjectBytes: 4096, MinTTLSeconds: 300, MaxTTLSeconds: 604800, Objects: objects})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(conn, store, Options{})
	body := validCreateSecretBody()
	delete(body, "ciphertext")
	body["size_bytes"] = 2048
	resp := performJSON(t, router, http.MethodPost, "/api/secrets", body)
	var created createSecretLargeResponse
	decodeBody(t, resp, &created)
	if resp.Code != http.StatusCreated || created.ManagementToken == "" || created.ManagementExpiresAt != created.ExpiresAt || !strings.Contains(created.Upload.URL, "/managed/secrets/"+created.ID) {
		t.Fatal("large creation omitted management capability or prefix")
	}
	status, err := store.Management(ctx, created.ID, created.ManagementToken)
	if err != nil || status.Status != "pending_upload" {
		t.Fatalf("large status: %+v %v", status, err)
	}
}
