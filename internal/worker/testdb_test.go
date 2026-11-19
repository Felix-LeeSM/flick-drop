package worker

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
)

type testWorkerDatabase struct {
	url string
}

var testSchemaCounter atomic.Int64

func openTestWorkerDatabase(t *testing.T) *testWorkerDatabase {
	t.Helper()
	ctx := context.Background()

	baseURL := os.Getenv("FLICK_TEST_POSTGRES_URL")
	if baseURL == "" {
		t.Skip("FLICK_TEST_POSTGRES_URL is not set; Postgres-backed worker tests run only against the compose/CI service")
	}

	schema := fmt.Sprintf("flick_worker_test_%d_%d", time.Now().UnixNano(), testSchemaCounter.Add(1))
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

	pool, err := sql.Open("pgx", scoped.String())
	if err != nil {
		_ = admin.Close()
		t.Fatalf("open postgres test pool: %v", err)
	}
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		_ = admin.Close()
		t.Fatalf("ping postgres test pool: %v", err)
	}
	if err := db.MigrateWorker(ctx, pool); err != nil {
		_ = pool.Close()
		_ = admin.Close()
		t.Fatalf("migrate worker schema %s: %v", schema, err)
	}

	t.Cleanup(func() {
		_ = pool.Close()
		_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("drop schema %s cascade", schema))
		_ = admin.Close()
	})

	return &testWorkerDatabase{url: scoped.String()}
}

func openRecoveryStore(t *testing.T, database *testWorkerDatabase) *ReceiptStore {
	t.Helper()

	conn, err := sql.Open("pgx", database.url)
	if err != nil {
		t.Fatalf("open postgres store pool: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	store, err := NewReceiptStore(conn)
	if err != nil {
		t.Fatalf("new receipt store: %v", err)
	}
	return store
}
