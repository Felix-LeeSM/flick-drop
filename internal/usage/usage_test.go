package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
)

func openUsageDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	conn, err := db.OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("migrate api: %v", err)
	}
	return conn
}

func counterValue(t *testing.T, conn *sql.DB, day, metric string) int64 {
	t.Helper()
	var count int64
	err := conn.QueryRow(`select count from usage_counters where day = ? and metric = ?`, day, metric).Scan(&count)
	if err == sql.ErrNoRows {
		return 0
	}
	if err != nil {
		t.Fatalf("read counter %s/%s: %v", day, metric, err)
	}
	return count
}

func TestIncrementUpsertsPerDayAndMetric(t *testing.T) {
	ctx := context.Background()
	conn := openUsageDB(t, ctx)
	day1 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 11, 11, 23, 30, 0, 0, time.UTC)

	if err := Increment(ctx, conn, day1, SecretCreatedText, 1); err != nil {
		t.Fatalf("increment: %v", err)
	}
	if err := Increment(ctx, conn, day1, SecretCreatedText, 1); err != nil {
		t.Fatalf("increment again: %v", err)
	}
	if err := Increment(ctx, conn, day1, BytesUploaded, 4096); err != nil {
		t.Fatalf("increment bytes: %v", err)
	}
	if err := Increment(ctx, conn, day2, SecretCreatedText, 1); err != nil {
		t.Fatalf("increment next day: %v", err)
	}

	if got := counterValue(t, conn, Day(day1), SecretCreatedText); got != 2 {
		t.Fatalf("same-day count = %d, want 2", got)
	}
	if got := counterValue(t, conn, Day(day1), BytesUploaded); got != 4096 {
		t.Fatalf("bytes count = %d, want 4096", got)
	}
	if got := counterValue(t, conn, Day(day2), SecretCreatedText); got != 1 {
		t.Fatalf("next-day count = %d, want 1", got)
	}
}

func TestSecretCreatedMetric(t *testing.T) {
	if got := SecretCreatedMetric("file"); got != SecretCreatedFile {
		t.Fatalf("file metric = %q, want %q", got, SecretCreatedFile)
	}
	if got := SecretCreatedMetric("text"); got != SecretCreatedText {
		t.Fatalf("text metric = %q, want %q", got, SecretCreatedText)
	}
}

func TestClampReportDays(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{in: 0, want: DefaultReportDays},
		{in: -5, want: DefaultReportDays},
		{in: 1, want: 1},
		{in: 30, want: 30},
		{in: 1000, want: MaxReportDays},
	}
	for _, c := range cases {
		if got := ClampReportDays(c.in); got != c.want {
			t.Fatalf("ClampReportDays(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestStoreReportZeroFillsWindowAndSumsAllTimeTotals(t *testing.T) {
	ctx := context.Background()
	conn := openUsageDB(t, ctx)
	store := NewStore(conn)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	// Today: two events on one counter. Yesterday: a bytes delta.
	// 40 days ago: an event outside the 30-day window — the series must hide
	// it and the all-time totals must still include it.
	if err := Increment(ctx, conn, now, SecretCreatedText, 1); err != nil {
		t.Fatal(err)
	}
	if err := Increment(ctx, conn, now, SecretCreatedText, 1); err != nil {
		t.Fatal(err)
	}
	if err := Increment(ctx, conn, now.AddDate(0, 0, -1), BytesUploaded, 4096); err != nil {
		t.Fatal(err)
	}
	if err := Increment(ctx, conn, now.AddDate(0, 0, -40), SecretOpened, 1); err != nil {
		t.Fatal(err)
	}

	report, err := store.Report(ctx, 30, now)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if report.Days != 30 {
		t.Fatalf("days = %d, want 30", report.Days)
	}
	if len(report.Series) != 30 {
		t.Fatalf("series length = %d, want 30", len(report.Series))
	}
	today := report.Series[29]
	if today.Day != Day(now) {
		t.Fatalf("last day = %q, want %q", today.Day, Day(now))
	}
	if today.Metrics[SecretCreatedText] != 2 {
		t.Fatalf("today count = %d, want 2", today.Metrics[SecretCreatedText])
	}
	yesterday := report.Series[28]
	if yesterday.Metrics[BytesUploaded] != 4096 {
		t.Fatalf("yesterday bytes = %d, want 4096", yesterday.Metrics[BytesUploaded])
	}
	// A day with no rows must still appear, zeroed, so a chart can plot it.
	quiet := report.Series[0]
	for metric, count := range quiet.Metrics {
		if count != 0 {
			t.Fatalf("quiet day carries %s = %d", metric, count)
		}
	}
	if quiet.Day != Day(now.AddDate(0, 0, -29)) {
		t.Fatalf("first day = %q, want %q", quiet.Day, Day(now.AddDate(0, 0, -29)))
	}
	if report.Totals[SecretOpened] != 1 {
		t.Fatalf("all-time opened total = %d, want 1 (outside window)", report.Totals[SecretOpened])
	}
	if report.Totals[SecretCreatedText] != 2 {
		t.Fatalf("all-time created total = %d, want 2", report.Totals[SecretCreatedText])
	}
}

func TestStoreReportLiveCounts(t *testing.T) {
	ctx := context.Background()
	conn := openUsageDB(t, ctx)
	store := NewStore(conn)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	for _, secret := range []struct {
		id         string
		state      string
		consumedAt string
		expiresAt  string
	}{
		{id: "sec-openable", state: "active", consumedAt: "", expiresAt: "2026-12-01T00:00:00Z"},
		{id: "sec-consumed", state: "active", consumedAt: "2026-11-09T00:00:00Z", expiresAt: "2026-12-01T00:00:00Z"},
		{id: "sec-expired", state: "active", consumedAt: "", expiresAt: "2026-11-01T00:00:00Z"},
		{id: "sec-pending", state: "pending_upload", consumedAt: "", expiresAt: "2026-12-01T00:00:00Z"},
	} {
		_, err := conn.Exec(`insert into secrets
			(id, kind, storage_backend, storage_key, size_bytes, state, consumed_at, expires_at, created_at, updated_at)
			values (?, 'text', 'sqlite_blob', ?, 10, ?, nullif(?, ''), ?, ?, ?)`,
			secret.id, secret.id, secret.state, secret.consumedAt, secret.expiresAt, secret.expiresAt, secret.expiresAt)
		if err != nil {
			t.Fatalf("insert secret %s: %v", secret.id, err)
		}
	}
	for _, request := range []struct {
		id        string
		state     string
		expiresAt string
	}{
		{id: "req-waiting", state: "waiting", expiresAt: "2026-12-01T00:00:00.000000000Z"},
		{id: "req-submitted", state: "submitted", expiresAt: "2026-12-01T00:00:00.000000000Z"},
		{id: "req-consumed", state: "consumed", expiresAt: "2026-12-01T00:00:00.000000000Z"},
		{id: "req-cancelled", state: "cancelled", expiresAt: "2026-12-01T00:00:00.000000000Z"},
		{id: "req-expired", state: "waiting", expiresAt: "2026-11-01T00:00:00.000000000Z"},
	} {
		_, err := conn.Exec(`insert into requests
			(id, public_key, fingerprint, submission_token_hash, retrieval_token_hash, state, kind, size_bytes, envelope_json, attempt_token_hash, attempt_body_hash, expires_at)
			values (?, 'synthetic-public-key', 'synthetic-fingerprint', zeroblob(32), zeroblob(32), ?,
				case when ? = 'submitted' then 'text' end,
				case when ? = 'submitted' then 10 end,
				case when ? = 'submitted' then '{}' end,
				case when ? = 'submitted' then zeroblob(32) end,
				case when ? = 'submitted' then zeroblob(32) end,
				?)`,
			request.id, request.state, request.state, request.state, request.state, request.state, request.state, request.expiresAt)
		if err != nil {
			t.Fatalf("insert request %s: %v", request.id, err)
		}
	}

	report, err := store.Report(ctx, DefaultReportDays, now)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	// Consumed, expired, and pending_upload rows stay out of inventory.
	if report.Live.ActiveSecrets != 1 {
		t.Fatalf("active secrets = %d, want 1", report.Live.ActiveSecrets)
	}
	// Cancelled, consumed, and expired links stay out.
	if report.Live.LiveRequests != 2 {
		t.Fatalf("live requests = %d, want 2", report.Live.LiveRequests)
	}
}
