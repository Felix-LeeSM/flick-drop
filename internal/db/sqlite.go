package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
)

func init() {
	// database/sql replaces discarded connections without calling OpenSQLite
	// again. The driver hook runs after DSN options on every physical connection.
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		for _, statement := range []string{
			"pragma busy_timeout = 5000",
			"pragma foreign_keys = on",
			"pragma journal_mode = wal",
		} {
			if _, err := conn.ExecContext(context.Background(), statement, nil); err != nil {
				return fmt.Errorf("apply sqlite setting %q: %w", statement, err)
			}
		}
		return nil
	})
}

func OpenSQLite(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	filename, err := sqliteFilename(path)
	if err != nil {
		return nil, err
	}
	if filename != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite parent directory: %w", err)
		}
	}

	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conn.SetMaxOpenConns(1)

	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	return conn, nil
}

// Decode only the filename used for directory creation. Pass the original DSN
// unchanged to SQLite so URI escaping and query options retain driver semantics.
func sqliteFilename(dsn string) (string, error) {
	if !strings.HasPrefix(dsn, "file:") {
		filename, _, _ := strings.Cut(dsn, "?")
		return filename, nil
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse sqlite URI: %w", err)
	}
	if u.Query().Get("mode") == "memory" {
		return ":memory:", nil
	}
	if u.Opaque != "" {
		return url.PathUnescape(u.Opaque)
	}
	return u.Path, nil
}
