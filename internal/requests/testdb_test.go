package requests

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

type testRequestsDatabase struct {
	db  *sql.DB
	url string
}

var testSchemaCounter atomic.Int64

func openTestRequestsDatabase(t *testing.T) *testRequestsDatabase {
	t.Helper()
	ctx := context.Background()

	baseURL := os.Getenv("FLICK_TEST_POSTGRES_URL")
	if baseURL == "" {
		t.Skip("FLICK_TEST_POSTGRES_URL is not set; Postgres-backed request tests run only against the compose/CI service")
	}

	schema := fmt.Sprintf("flick_api_test_requests_%d_%d", time.Now().UnixNano(), testSchemaCounter.Add(1))
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

	return &testRequestsDatabase{db: pool, url: scoped.String()}
}

func injectFailureTrigger(t *testing.T, conn *sql.DB, name, table, action, when string) {
	t.Helper()
	fn := name + "_fn"
	if _, err := conn.Exec(`create function ` + fn + `() returns trigger language plpgsql as $fn$
		begin
			raise exception 'injected storage failure';
		end;
		$fn$`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(rowTriggerDDL(name, table, action, when, `execute function `+fn+`()`)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(`drop trigger ` + name + ` on ` + table)
		_, _ = conn.Exec(`drop function ` + fn)
	})
}

func injectDeferredCommitFailure(t *testing.T, conn *sql.DB, name, table, action, when string) {
	t.Helper()
	if _, err := conn.Exec(`create table deferred_failure (request_id text references requests(id) deferrable initially deferred)`); err != nil {
		t.Fatal(err)
	}
	fn := name + "_fn"
	if _, err := conn.Exec(`create function ` + fn + `() returns trigger language plpgsql as $fn$
		begin
			insert into deferred_failure values ('missing-parent');
			return null;
		end;
		$fn$`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(rowTriggerDDL(name, table, action, when, `execute function `+fn+`()`)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(`drop table deferred_failure`)
		_, _ = conn.Exec(`drop trigger ` + name + ` on ` + table)
		_, _ = conn.Exec(`drop function ` + fn)
	})
}

func rowTriggerDDL(name, table, action, when, body string) string {
	ddl := `create trigger ` + name + ` ` + action + ` on ` + table + ` for each row`
	if when != "" {
		ddl += ` when (` + when + `)`
	}
	return ddl + ` ` + body
}
