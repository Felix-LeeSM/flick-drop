// Package usage: counters are the only durable usage history (secrets/requests rows are hard-deleted).
// Rows carry counts only, never IDs, IPs, user agents, or content metadata, and are never deleted.
package usage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Keep the set closed: a new metric needs a write point in the same change.
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

// Mirrors the secrets table CHECK vocabulary; importing secrets would cycle.
const secretKindFile = "file"

// Unknown kinds map to the text counter so the increment is never dropped.
func SecretCreatedMetric(kind string) string {
	if kind == secretKindFile {
		return SecretCreatedFile
	}
	return SecretCreatedText
}

// Takes the caller's tx so the increment commits or rolls back with the state change.
type Counter interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// UTC; callers pass the store clock so counters bucket on the TTL clock.
func Day(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// Deletion and cancellation are deliberately not subtracted: counters record events, not inventory.
func Increment(ctx context.Context, q Counter, t time.Time, metric string, delta int64) error {
	_, err := q.ExecContext(ctx, `insert into usage_counters (day, metric, count)
		values ($1, $2, $3)
		on conflict(day, metric) do update set count = usage_counters.count + excluded.count`,
		Day(t), metric, delta)
	if err != nil {
		return fmt.Errorf("increment usage counter %s: %w", metric, err)
	}
	return nil
}
