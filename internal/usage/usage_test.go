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
