package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsHandlerExposesFormat(t *testing.T) {
	SecretCreated.WithLabelValues("text", "sqlite_blob").Inc()
	SecretOpened.Inc()
	SecretReaped.WithLabelValues("expired").Inc()
	JobsProcessed.WithLabelValues("delete_secret", "succeeded").Inc()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusOK {
		t.Fatalf("status = %d, want %d", got, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q, want text/plain prefix", ct)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"# HELP flick_secret_created_total",
		"# TYPE flick_secret_created_total counter",
		"flick_secret_created_total{kind=\"text\",storage=\"sqlite_blob\"} 1",
		"# HELP flick_secret_opened_total",
		"flick_secret_opened_total 1",
		"# HELP flick_secret_reaped_total",
		"flick_secret_reaped_total{reason=\"expired\"} 1",
		"# HELP flick_jobs_processed_total",
		"flick_jobs_processed_total{kind=\"delete_secret\",outcome=\"succeeded\"} 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// Security invariant: the exposition never carries secret-material terms.
func TestMetricsHandlerNoSecretTerms(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, req)
	body := rec.Body.String()

	for _, forbidden := range []string{"passphrase", "ciphertext", "derived_key", "secret_key", "access_proof"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Errorf("metrics body must not contain %q (security-model.md):\n%s", forbidden, body)
		}
	}
}
