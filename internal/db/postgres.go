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

const (
	PostgresMaxOpenConns    = 8
	PostgresMaxIdleConns    = 4
	PostgresConnMaxLifetime = 30 * time.Minute
)

// A new pod may be unreachable for a few seconds (k3s kube-router NetworkPolicy)
// or Postgres may be restarting; a wrong DSN still fails once the wait is over.
var (
	postgresStartupWait  = 30 * time.Second
	postgresStartupRetry = time.Second
)

func OpenPostgres(ctx context.Context, dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("postgres DSN is required")
	}

	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres DSN: %w", err)
	}
	// pgx v5 decodes timestamptz into time.Local; the JSON/header contract is UTC.
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

	if err := pingUntilReady(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return conn, nil
}

func pingUntilReady(ctx context.Context, conn *sql.DB) error {
	deadline := time.Now().Add(postgresStartupWait)
	for {
		err := conn.PingContext(ctx)
		if err == nil || ctx.Err() != nil || time.Now().Add(postgresStartupRetry).After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(postgresStartupRetry):
		}
	}
}
