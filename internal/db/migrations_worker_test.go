package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

// Skips without FLICK_TEST_POSTGRES_URL.
func openTestWorkerDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	baseURL := os.Getenv("FLICK_TEST_POSTGRES_URL")
	if baseURL == "" {
		t.Skip("FLICK_TEST_POSTGRES_URL is not set; Postgres-backed worker migration tests run only against the compose/CI service")
	}

	schema := fmt.Sprintf("flick_db_test_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatalf("open postgres admin pool: %v", err)
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("create schema %s", schema)); err != nil {
		_ = admin.Close()
		t.Fatalf("create test schema %s: %v", schema, err)
	}

	// pgx v5 forwards unknown URL query params as runtime parameters (search_path).
	scoped, err := url.Parse(baseURL)
	if err != nil {
		_ = admin.Close()
		t.Fatalf("parse FLICK_TEST_POSTGRES_URL: %v", err)
	}
	query := scoped.Query()
	query.Set("search_path", schema)
	scoped.RawQuery = query.Encode()

	conn, err := OpenPostgres(ctx, scoped.String())
	if err != nil {
		_ = admin.Close()
		t.Fatalf("open postgres test pool: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("drop schema %s cascade", schema))
		_ = admin.Close()
	})

	return conn
}

func openTestAPIDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	baseURL := os.Getenv("FLICK_TEST_POSTGRES_URL")
	if baseURL == "" {
		t.Skip("FLICK_TEST_POSTGRES_URL is not set; Postgres-backed api migration tests run only against the compose/CI service")
	}

	schema := fmt.Sprintf("flick_api_db_test_%d", time.Now().UnixNano())
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

	conn, err := OpenPostgres(ctx, scoped.String())
	if err != nil {
		_ = admin.Close()
		t.Fatalf("open postgres test pool: %v", err)
	}
	if err := MigrateAPI(ctx, conn); err != nil {
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

func TestMigrateWorkerCreatesJobReceipts(t *testing.T) {
	ctx := context.Background()
	conn := openTestWorkerDatabase(t)

	if err := MigrateWorker(ctx, conn); err != nil {
		t.Fatalf("migrate worker: %v", err)
	}

	_, err := conn.ExecContext(ctx, `insert into job_receipts (
		job_id, kind, state, attempts, first_seen_at, updated_at
	) values ($1, $2, $3, 0, $4, $5)`,
		"job_1",
		"delete_secret",
		"processing",
		time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("insert job receipt: %v", err)
	}

	_, err = conn.ExecContext(ctx, `insert into job_attempts (
		job_id, attempt, started_at, result
	) values ($1, 1, $2, $3)`,
		"job_1",
		time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC),
		"running",
	)
	if err != nil {
		t.Fatalf("insert job attempt: %v", err)
	}

	_, err = conn.ExecContext(ctx, `insert into dead_letters (
		job_id, kind, payload_json, error, created_at
	) values ($1, $2, $3, $4, $5)
	on conflict(job_id) do update set error = excluded.error`,
		"job_1",
		"delete_secret",
		`{"job_id":"job_1","kind":"delete_secret","secret_id":"sec_1","requested_at":"2026-06-17T00:00:00Z"}`,
		"failed permanently",
		time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("insert dead letter: %v", err)
	}
}
