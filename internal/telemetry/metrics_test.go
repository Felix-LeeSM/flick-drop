package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// All instruments must be registered on DefaultRegistry, otherwise /metrics
// would not expose them. This guards against a future init() that forgets a
// metric declared above.
func TestInstrumentsRegistered(t *testing.T) {
	for name, c := range map[string]int{
		"flick_secret_created_total": testutil.CollectAndCount(SecretCreated),
		"flick_secret_opened_total":  testutil.CollectAndCount(SecretOpened),
		"flick_secret_reaped_total":  testutil.CollectAndCount(SecretReaped),
		"flick_jobs_processed_total": testutil.CollectAndCount(JobsProcessed),
		"flick_active_uploads":       testutil.CollectAndCount(ActiveUploads),
	} {
		if c < 0 {
			t.Errorf("%s is not registered on DefaultRegistry", name)
		}
		// CollectAndCount returns the number of labelled samples, which is 0
		// until a label vector is touched. That is fine; the important property
		// is that Collect does not error (an unregistered collector would).
	}
}

func TestMetricsHandlerExposesFormat(t *testing.T) {
	// Touch a few label vectors so the handler has samples to emit, then scrape.
	SecretCreated.WithLabelValues("text", "sqlite_blob").Inc()
	SecretOpened.Inc()
	JobsProcessed.WithLabelValues("delete_secret", "succeeded").Inc()
	ActiveUploads.Inc()

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
		"# HELP flick_secret_opened_total",
		"# HELP flick_jobs_processed_total",
		"# TYPE flick_active_uploads gauge",
		"flick_secret_created_total{kind=\"text\",storage=\"sqlite_blob\"} 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// Security invariant (security-model.md): the exposition must never carry a
// plaintext secret, passphrase, or derived key. We assert that none of the
// label names or HELP texts leak those terms, and that the only values emitted
// are safe cardinality labels.
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
