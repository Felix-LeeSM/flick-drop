package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
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

	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres DSN: %w", err)
	}
	// pgx v5 decodes timestamptz into the Go process location (time.Local)
	// whatever the session TimeZone is. The stores hand scanned times straight
	// to JSON and HTTP headers, whose contract is UTC ("Z"), so every
	// connection decodes timestamptz as UTC regardless of the host's TZ.
	conn := stdlib.OpenDB(*config, stdlib.OptionAfterConnect(func(_ context.Context, c *pgx.Conn) error {
		c.TypeMap().RegisterType(&pgtype.Type{
			Name:  "timestamptz",
			OID:   pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
		})
		return nil
	}))
	conn.SetMaxOpenConns(PostgresMaxOpenConns)
	conn.SetMaxIdleConns(PostgresMaxIdleConns)
	conn.SetConnMaxLifetime(PostgresConnMaxLifetime)

	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return conn, nil
}
