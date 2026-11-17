package events

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

// openAPITestDatabase opens an isolated Postgres schema for one test and runs
// MigrateAPI in it. Without FLICK_TEST_POSTGRES_URL the test skips: the
// compose/CI service provides the URL (scripts/dev/postgres-init.sql,
// .github/workflows/ci.yml).
func openAPITestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	baseURL := os.Getenv("FLICK_TEST_POSTGRES_URL")
	if baseURL == "" {
		t.Skip("FLICK_TEST_POSTGRES_URL is not set; Postgres-backed tests run only against the compose/CI service")
	}

	schema := fmt.Sprintf("flick_api_test_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatalf("open postgres admin pool: %v", err)
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("create schema %s", schema)); err != nil {
		_ = admin.Close()
		t.Fatalf("create test schema %s: %v", schema, err)
	}

	// pgx v5 passes unknown URL query parameters to the server as runtime
	// parameters, so every pooled connection lands in the test schema.
	scoped, err := url.Parse(baseURL)
	if err != nil {
		_ = admin.Close()
		t.Fatalf("parse FLICK_TEST_POSTGRES_URL: %v", err)
	}
	query := scoped.Query()
	query.Set("search_path", schema)
	scoped.RawQuery = query.Encode()

	// OpenPostgres (not a raw sql.Open) so the pool pins the session time
	// zone to UTC like the production pools do.
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
