package httpapi

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRevokeHTTPContract(t *testing.T) {
	for _, outcome := range []string{"cancelled", "opened", "locked"} {
		t.Run(outcome, func(t *testing.T) {
			f := newTestRouterFixture(t, Options{OpenRatePerMinute: 100})
			resp := performJSON(t, f.router, http.MethodPost, "/api/secrets", validCreateSecretBody())
			var created createSecretResponse
			decodeBody(t, resp, &created)
			path := "/api/secrets/" + created.ID
			headers := map[string]string{"Authorization": "Bearer " + created.ManagementToken}
			if outcome != "cancelled" {
				attempts, proof := 1, "proof"
				if outcome == "locked" {
					attempts, proof = 5, "wrong"
				}
				for range attempts {
					performJSON(t, f.router, http.MethodPost, path+"/open", map[string]string{"access_proof": base64.StdEncoding.EncodeToString([]byte(proof))})
				}
			}
			for _, body := range []string{"{", "null", `{"unexpected":1}`, "{} {}"} {
				req := httptest.NewRequest(http.MethodPost, path+"/revoke", strings.NewReader(body))
				req.Header.Set("Authorization", headers["Authorization"])
				rec := httptest.NewRecorder()
				f.router.ServeHTTP(rec, req)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("malformed body: %d", rec.Code)
				}
			}
			for range 2 {
				resp = performJSON(t, f.router, http.MethodPost, path+"/revoke", map[string]any{}, headers)
				var body map[string]any
				decodeBody(t, resp, &body)
				if resp.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("cancellation response cacheable")
				}
				snapshot := body
				if outcome == "cancelled" {
					if resp.Code != http.StatusOK {
						t.Fatalf("cancel: %d", resp.Code)
					}
				} else {
					if resp.Code != http.StatusConflict {
						t.Fatalf("terminal cancel: %d", resp.Code)
					}
					if body["error"].(map[string]any)["code"] != "not_cancellable" {
						t.Fatal("wrong conflict code")
					}
					snapshot = body["status"].(map[string]any)
				}
				if len(snapshot) != 5 || snapshot["status"] != outcome || snapshot["can_cancel"] != false || snapshot["expires_at"] != snapshot["management_expires_at"] {
					t.Fatalf("unsafe or invalid snapshot: %v", snapshot)
				}
				if strings.Contains(resp.Body.String(), created.ManagementToken) {
					t.Fatal("raw capability returned twice")
				}
			}
			if outcome == "cancelled" {
				resp = performJSON(t, f.router, http.MethodPost, path+"/open", map[string]string{"access_proof": base64.StdEncoding.EncodeToString([]byte("proof"))})
				if resp.Code != http.StatusNotFound {
					t.Fatalf("cancelled open: %d", resp.Code)
				}
			}
			var rejected string
			for _, authorization := range []string{"", "Bearer wrong", headers["Authorization"] + "="} {
				resp = performJSON(t, f.router, http.MethodPost, path+"/revoke", map[string]any{}, map[string]string{"Authorization": authorization})
				if resp.Code != http.StatusNotFound {
					t.Fatalf("invalid capability: %d", resp.Code)
				}
				if rejected != "" && rejected != resp.Body.String() {
					t.Fatal("capability rejection differs")
				}
				rejected = resp.Body.String()
			}
			_, err := f.db.Exec(`update secret_management set expires_at = '2000-01-01T00:00:00.000000000Z'`)
			if err != nil {
				t.Fatal(err)
			}
			resp = performJSON(t, f.router, http.MethodPost, path+"/revoke", map[string]any{}, headers)
			if resp.Body.String() != rejected {
				t.Fatal("expired capability distinguished")
			}
		})
	}
}

func TestRevokeSharesManagementRateLimit(t *testing.T) {
	f := newTestRouterFixture(t, Options{OpenRatePerMinute: 1})
	resp := performJSON(t, f.router, http.MethodPost, "/api/secrets", validCreateSecretBody())
	var created createSecretResponse
	decodeBody(t, resp, &created)
	headers := map[string]string{"Authorization": "Bearer " + created.ManagementToken}
	path := "/api/secrets/" + created.ID
	resp = performJSON(t, f.router, http.MethodGet, path+"/management", nil, headers)
	if resp.Code != http.StatusOK {
		t.Fatal(resp.Code)
	}
	resp = performJSON(t, f.router, http.MethodPost, path+"/revoke", map[string]any{}, headers)
	if resp.Code != http.StatusTooManyRequests || resp.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("status/revoke limit was not shared")
	}
}

func TestObjectReconciliationAcknowledgementIsAuthenticatedAndFenced(t *testing.T) {
	f := newTestRouterFixture(t, Options{InternalToken: "test-token"})
	_, err := f.db.Exec(`insert into object_reconciliation_pending values ('managed/secrets/key','new-job')`)
	if err != nil {
		t.Fatal(err)
	}
	path := "/internal/object-reconciliation/ack"
	body := map[string]string{"job_id": "new-job", "object_key": "managed/secrets/key"}
	resp := performJSON(t, f.router, http.MethodPost, path, body)
	if resp.Code != http.StatusUnauthorized {
		t.Fatal("missing internal token accepted")
	}
	headers := map[string]string{"X-Flick-Internal-Token": "test-token"}
	for _, job := range []string{"old-job", "new-job", "new-job"} {
		body["job_id"] = job
		resp = performJSON(t, f.router, http.MethodPost, path, body, headers)
		if resp.Code != http.StatusNoContent {
			t.Fatalf("ack: %d", resp.Code)
		}
		var pending int
		if err := f.db.QueryRow(`select count(*) from object_reconciliation_pending`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		want := 0
		if job == "old-job" {
			want = 1
		}
		if pending != want {
			t.Fatalf("stale ack changed claim: %d", pending)
		}
	}
	body["object_key"] = "managed/unowned/foreign"
	resp = performJSON(t, f.router, http.MethodPost, path, body, headers)
	if resp.Code != http.StatusBadRequest {
		t.Fatal("other namespace accepted")
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	body["object_key"] = "managed/secrets/key"
	resp = performJSON(t, f.router, http.MethodPost, path, body, headers)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatal("database error acknowledged cleanup")
	}
}
