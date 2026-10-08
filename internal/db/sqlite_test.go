package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSQLiteAppliesRequiredSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "api.db")

	conn, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	if got := queryPragma(t, conn, "foreign_keys"); got != "1" {
		t.Fatalf("foreign_keys = %s, want 1", got)
	}
	if got := queryPragma(t, conn, "busy_timeout"); got != "5000" {
		t.Fatalf("busy_timeout = %s, want 5000", got)
	}
	if got := queryPragma(t, conn, "journal_mode"); got != "wal" {
		t.Fatalf("journal_mode = %s, want wal", got)
	}
}

func TestOpenSQLiteReplacementKeepsRequiredSettings(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"file", "uri", "memory", "memory_uri", "named_memory_uri"} {
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "nested # directory", "api #.db")
			dsn, mode := filename, "wal"
			switch name {
			case "uri":
				filename = filepath.Join(t.TempDir(), "nested ? directory", "api ?#.db")
				dsn = (&url.URL{Scheme: "file", Path: filename, RawQuery: "mode=rwc&_pragma=foreign_keys(0)&_pragma=busy_timeout(0)"}).String()
			case "memory":
				dsn, mode = ":memory:", "memory"
			case "memory_uri":
				dsn, mode = "file::memory:?cache=shared", "memory"
			case "named_memory_uri":
				dsn, mode = "file:replacement-test?mode=memory&cache=shared", "memory"
			}
			conn, err := OpenSQLite(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			for i := 0; i < 2; i++ {
				for pragma, want := range map[string]string{"foreign_keys": "1", "busy_timeout": "5000", "journal_mode": mode} {
					if got := queryPragma(t, conn, pragma); got != want {
						t.Fatalf("connection %d: %s=%s, want %s", i, pragma, got, want)
					}
				}
				if i == 0 {
					physical, err := conn.Conn(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if err := physical.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
						t.Fatalf("discard connection: %v", err)
					}
					_ = physical.Close()
				}
			}
			if mode == "wal" {
				if _, err := os.Stat(filename); err != nil {
					t.Fatalf("SQLite did not preserve filename: %v", err)
				}
			}
			// Every replacement must enforce the actual cascade, not just report
			// the pragma. Fresh in-memory connections intentionally start empty.
			if _, err := conn.Exec(`create table parent(id integer primary key); create table child(id integer references parent(id) on delete cascade); insert into parent values(1); insert into child values(1); delete from parent`); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := conn.QueryRow(`select count(*) from child`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("replacement cascade left %d child rows: %v", count, err)
			}
		})
	}
}

func queryPragma(t *testing.T, conn *sql.DB, name string) string {
	t.Helper()

	var value string
	if err := conn.QueryRow("pragma " + name).Scan(&value); err != nil {
		t.Fatalf("query pragma %s: %v", name, err)
	}
	return value
}
