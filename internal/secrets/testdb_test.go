package secrets

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

type testAPIDatabase struct {
	url string
}

var testAPISchemaCounter atomic.Int64

func openTestAPIDatabase(t *testing.T) *testAPIDatabase {
	t.Helper()
	ctx := context.Background()

	baseURL := os.Getenv("FLICK_TEST_POSTGRES_URL")
	if baseURL == "" {
		t.Skip("FLICK_TEST_POSTGRES_URL is not set; Postgres-backed secrets tests run only against the compose/CI service")
	}

	schema := fmt.Sprintf("flick_api_test_%d_%d", time.Now().UnixNano(), testAPISchemaCounter.Add(1))
	admin, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatalf("open postgres admin pool: %v", err)
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("create schema %s", schema)); err != nil {
		_ = admin.Close()
		t.Fatalf("create test schema %s: %v", schema, err)
	}

	// pgx v5 passes unknown URL query parameters as runtime parameters, so every pooled connection lands in the test schema.
	scoped, err := url.Parse(baseURL)
	if err != nil {
		_ = admin.Close()
		t.Fatalf("parse FLICK_TEST_POSTGRES_URL: %v", err)
	}
	query := scoped.Query()
	query.Set("search_path", schema)
	scoped.RawQuery = query.Encode()

	pool, err := db.OpenPostgres(ctx, scoped.String())
	if err != nil {
		_ = admin.Close()
		t.Fatalf("open postgres test pool: %v", err)
	}
	if err := db.MigrateAPI(ctx, pool); err != nil {
		_ = pool.Close()
		_ = admin.Close()
		t.Fatalf("migrate api schema %s: %v", schema, err)
	}

	t.Cleanup(func() {
		_ = pool.Close()
		_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("drop schema %s cascade", schema))
		_ = admin.Close()
	})

	return &testAPIDatabase{url: scoped.String()}
}

func (d *testAPIDatabase) openPool(t *testing.T) *sql.DB {
	t.Helper()

	conn, err := db.OpenPostgres(context.Background(), d.url)
	if err != nil {
		t.Fatalf("open postgres store pool: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return conn
}

func openTestDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()

	database := openTestAPIDatabase(t)
	return database.openPool(t)
}
