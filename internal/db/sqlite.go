package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func OpenSQLite(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite parent directory: %w", err)
		}
	}

	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conn.SetMaxOpenConns(1)

	if err := applyPragmas(ctx, conn, path); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	return conn, nil
}

func applyPragmas(ctx context.Context, conn *sql.DB, path string) error {
	statements := []string{
		"pragma foreign_keys = on",
		"pragma busy_timeout = 5000",
	}
	if path != ":memory:" {
		statements = append(statements, "pragma journal_mode = wal")
	}
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply sqlite setting %q: %w", statement, err)
		}
	}
	return nil
}
