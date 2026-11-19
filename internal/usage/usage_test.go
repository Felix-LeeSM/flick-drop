package usage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
)

func openUsageDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()

	baseURL := os.Getenv("FLICK_TEST_POSTGRES_URL")
	if baseURL == "" {
		t.Skip("FLICK_TEST_POSTGRES_URL is not set; Postgres-backed usage tests run only against the compose/CI service")
	}

	schema := fmt.Sprintf("flick_usage_test_%d", time.Now().UnixNano())
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

func counterValue(t *testing.T, conn *sql.DB, day, metric string) int64 {
	t.Helper()
	var count int64
	err := conn.QueryRow(`select count from usage_counters where day = $1 and metric = $2`, day, metric).Scan(&count)
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

// 하루 업로드 바이트 합계는 int32(약 2 GiB)를 넘는다(256 MiB 파일 아홉 개).
func TestIncrementAccumulatesPastInt32(t *testing.T) {
	ctx := context.Background()
	conn := openUsageDB(t, ctx)
	day := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

	if err := Increment(ctx, conn, day, BytesUploaded, 2_000_000_000); err != nil {
		t.Fatalf("increment: %v", err)
	}
	if err := Increment(ctx, conn, day, BytesUploaded, 268_435_456); err != nil {
		t.Fatalf("increment past int32: %v", err)
	}

	if got, want := counterValue(t, conn, Day(day), BytesUploaded), int64(2_268_435_456); got != want {
		t.Fatalf("bytes count = %d, want %d", got, want)
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
