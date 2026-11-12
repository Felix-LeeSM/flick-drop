// Package usage owns the api.db usage_counters table: small daily aggregate
// counters written inside the same transaction as the state change they
// describe. Because secrets and requests rows are hard-deleted when they
// expire, these counters are the only durable usage history. Rows carry
// counts only — never IDs, IPs, user agents, or content metadata — and are
// never deleted, which keeps the table at one row per day per metric.
package usage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Metric names stored in usage_counters.metric. The secrets and requests
// stores write these constants. Keep the set closed — a new metric needs a
// write point in the same change.
const (
	SecretCreatedText = "secret_created_text"
	SecretCreatedFile = "secret_created_file"
	SecretOpened      = "secret_opened"
	SecretOpenFailed  = "secret_open_failed"
	BytesUploaded     = "bytes_uploaded"
	RequestCreated    = "request_created"
	RequestSubmitted  = "request_submitted"
	RequestConsumed   = "request_consumed"
)

// The kind strings are the secrets table's CHECK vocabulary
// ('text'/'file'); usage cannot import the secrets package (it would
// cycle), so the literals live here with that comment as the link.
const secretKindFile = "file"

// SecretCreatedMetric maps a secret kind to its created counter. Unknown
// kinds cannot reach this path: the secrets store validates kind before
// inserting, but mapping them to the text counter keeps the increment
// total rather than dropping the event.
func SecretCreatedMetric(kind string) string {
	if kind == secretKindFile {
		return SecretCreatedFile
	}
	return SecretCreatedText
}

// Counter is the subset of *sql.Tx/*sql.DB the increment needs. It takes the
// caller's transaction handle rather than *sql.DB because the increment must
// commit or roll back with the state change it describes, and the SQLite pool
// is a single connection (db/sqlite.go SetMaxOpenConns(1)) — an out-of-band
// write would wait on that transaction's own connection and deadlock.
type Counter interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Day renders the UTC calendar day (YYYY-MM-DD) a counter increment belongs
// to. All write points hold the store clock, so the counters bucket on the
// same clock the TTLs use.
func Day(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// Increment adds delta to the (day, metric) counter inside the caller's
// transaction. Deletion and cancellation are deliberately not subtracted:
// the counters record committed usage events, not current inventory.
func Increment(ctx context.Context, q Counter, t time.Time, metric string, delta int64) error {
	_, err := q.ExecContext(ctx, `insert into usage_counters (day, metric, count)
		values (?, ?, ?)
		on conflict(day, metric) do update set count = count + excluded.count`,
		Day(t), metric, delta)
	if err != nil {
		return fmt.Errorf("increment usage counter %s: %w", metric, err)
	}
	return nil
}
