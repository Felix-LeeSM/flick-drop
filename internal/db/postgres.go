package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// Pool defaults for one Flick service instance. Postgres removed the SQLite
// single-writer constraint, so the pool can serve concurrent replicas; the
// API runs two replicas at M15, and each role talks only to its own logical
// database (docs/architecture/database-schema.md, "Ownership").
const (
	PostgresMaxOpenConns    = 8
	PostgresMaxIdleConns    = 4
	PostgresConnMaxLifetime = 30 * time.Minute
)

func OpenPostgres(ctx context.Context, dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("postgres DSN is required")
	}

	conn, err := sql.Open("pgx", pinSessionUTC(dsn))
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	conn.SetMaxOpenConns(PostgresMaxOpenConns)
	conn.SetMaxIdleConns(PostgresMaxIdleConns)
	conn.SetConnMaxLifetime(PostgresConnMaxLifetime)

	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return conn, nil
}

// pinSessionUTC defaults the DSN's session time zone to UTC: a scanned
// timestamptz carries the session zone with it, and every Flick boundary that
// formats a time (Flick-* HTTP headers, management and request owner views,
// usage-counter day buckets) expects UTC. A DSN that sets TimeZone explicitly
// wins; a non-URL (keyword) DSN passes through untouched.
func pinSessionUTC(dsn string) string {
	scoped, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	query := scoped.Query()
	if query.Get("TimeZone") != "" {
		return dsn
	}
	query.Set("TimeZone", "UTC")
	scoped.RawQuery = query.Encode()
	return scoped.String()
}
