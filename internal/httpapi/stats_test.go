package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/secrets"
	"github.com/Felix-LeeSM/flick-drop/internal/usage"
)

// newStatsRouter builds a router wired for the stats endpoint on its own
// database, so each auth case starts with a fresh rate limiter.
func newStatsRouter(t *testing.T, statsToken string) (*sql.DB, http.Handler) {
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
	router := NewRouter(conn, store, Options{
		PayloadInlineMaxBytes: 1024,
		StatsToken:            statsToken,
		UsageStore:            usage.NewStore(conn),
	})
	return conn, router
}

func getStats(t *testing.T, router http.Handler, authorization string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// The endpoint hides its own existence: every failure mode a stranger can
// produce — no token, wrong token, a token against a deployment with none
// configured, or a router with no report store — answers the same 404.
func TestStatsEndpointFailsClosedOnEveryAuthFailure(t *testing.T) {
	ctx := context.Background()

	_, configured := newStatsRouter(t, "stats-token-1")
	if got := getStats(t, configured, "").Code; got != http.StatusNotFound {
		t.Fatalf("missing token status = %d, want 404", got)
	}
	if got := getStats(t, configured, "Bearer wrong-token").Code; got != http.StatusNotFound {
		t.Fatalf("wrong token status = %d, want 404", got)
	}
	// A token that merely extends the configured value must not authenticate:
	// the comparison covers the whole presented token.
	if got := getStats(t, configured, "Bearer stats-token-1-extra").Code; got != http.StatusNotFound {
		t.Fatalf("prefix token status = %d, want 404", got)
	}

	_, empty := newStatsRouter(t, "")
	if got := getStats(t, empty, "Bearer anything").Code; got != http.StatusNotFound {
		t.Fatalf("empty configured token status = %d, want 404", got)
	}

	conn := openHTTPTestDB(t, ctx)
	store, err := secrets.NewStore(conn, secrets.StoreOptions{
		PayloadInlineMaxBytes: 1024,
		MinTTLSeconds:         300,
		MaxTTLSeconds:         604800,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	unwired := NewRouter(conn, store, Options{PayloadInlineMaxBytes: 1024, StatsToken: "stats-token-2"})
	if got := getStats(t, unwired, "Bearer stats-token-2").Code; got != http.StatusNotFound {
		t.Fatalf("unwired usage store status = %d, want 404", got)
	}
}

func TestStatsEndpointReturnsReportForValidToken(t *testing.T) {
	conn, router := newStatsRouter(t, "stats-token-3")
	ctx := context.Background()
	now := time.Now().UTC()

	if err := usage.Increment(ctx, conn, now, usage.SecretCreatedText, 2); err != nil {
		t.Fatalf("increment: %v", err)
	}
	if err := usage.Increment(ctx, conn, now, usage.BytesUploaded, 4096); err != nil {
		t.Fatalf("increment bytes: %v", err)
	}

	rec := getStats(t, router, "Bearer stats-token-3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", got)
	}

	var report usage.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Days != usage.DefaultReportDays {
		t.Fatalf("days = %d, want %d", report.Days, usage.DefaultReportDays)
	}
	if len(report.Series) != usage.DefaultReportDays {
		t.Fatalf("series length = %d, want %d", len(report.Series), usage.DefaultReportDays)
	}
	today := report.Series[len(report.Series)-1]
	if today.Metrics[usage.SecretCreatedText] != 2 || today.Metrics[usage.BytesUploaded] != 4096 {
		t.Fatalf("today metrics = %v, want created=2 bytes=4096", today.Metrics)
	}
	if report.Totals[usage.SecretCreatedText] != 2 {
		t.Fatalf("totals created = %d, want 2", report.Totals[usage.SecretCreatedText])
	}
}

func TestStatsEndpointClampsDaysParameter(t *testing.T) {
	_, router := newStatsRouter(t, "stats-token-4")

	fetch := func(query string) usage.Report {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/stats"+query, nil)
		req.Header.Set("Authorization", "Bearer stats-token-4")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d for %s", rec.Code, query)
		}
		var report usage.Report
		if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
			t.Fatalf("decode report for %s: %v", query, err)
		}
		return report
	}
	if got := fetch("?days=9999").Days; got != usage.MaxReportDays {
		t.Fatalf("days=9999 clamped to %d, want %d", got, usage.MaxReportDays)
	}
	if got := fetch("?days=7").Days; got != 7 {
		t.Fatalf("days=7 read as %d, want 7", got)
	}
	if got := fetch("?days=garbage").Days; got != usage.DefaultReportDays {
		t.Fatalf("malformed days fell back to %d, want %d", got, usage.DefaultReportDays)
	}
}
