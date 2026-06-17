package db

import (
	"context"
	"database/sql"
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

func queryPragma(t *testing.T, conn *sql.DB, name string) string {
	t.Helper()

	var value string
	if err := conn.QueryRow("pragma " + name).Scan(&value); err != nil {
		t.Fatalf("query pragma %s: %v", name, err)
	}
	return value
}
