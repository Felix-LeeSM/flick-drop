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
// stores write these constants; the stats report labels them. Keep the set
// closed — a new metric needs a write point and a label together.
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
// the counters record committed usage events, not current inventory (the
// report's live counts cover inventory).
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

const (
	DefaultReportDays = 30
	MaxReportDays     = 365
)

// ClampReportDays bounds the ?days= parameter: unset, zero, or negative falls
// to the default window; anything above the cap clamps to it.
func ClampReportDays(days int) int {
	if days <= 0 {
		return DefaultReportDays
	}
	if days > MaxReportDays {
		return MaxReportDays
	}
	return days
}

// DailyCount is one calendar day of the series. Metrics only carries the
// metrics with a nonzero count that day; the report zero-fills the window so
// a chart can plot every day.
type DailyCount struct {
	Day     string           `json:"day"`
	Metrics map[string]int64 `json:"metrics"`
}

// LiveCounts is current inventory, read live from the secrets and requests
// tables rather than the counters: active_secrets counts rows a recipient
// could still open (state 'active', not consumed, not expired); live_requests
// counts request links still waiting for or holding a submission. Consumed or
// cancelled rows awaiting reaping stay excluded so the numbers read as
// inventory, not history.
type LiveCounts struct {
	ActiveSecrets int64 `json:"active_secrets"`
	LiveRequests  int64 `json:"live_requests"`
}

// Report is the admin stats payload: a zero-filled daily series, all-time
// totals, and live inventory.
type Report struct {
	Days   int              `json:"days"`
	Series []DailyCount     `json:"series"`
	Totals map[string]int64 `json:"totals"`
	Live   LiveCounts       `json:"live"`
}

// Store reads usage_counters and the live inventory for the admin stats
// report. It only reads; the secrets and requests stores remain the writers
// of their tables and of the counters.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Report aggregates the counters for the last days UTC days (inclusive of
// today), the all-time totals, and the live inventory. The three queries run
// outside a transaction: the report is informational and a torn read across
// them is acceptable for an admin view.
func (s *Store) Report(ctx context.Context, days int, now time.Time) (Report, error) {
	days = ClampReportDays(days)
	windowStart := Day(now.AddDate(0, 0, -(days - 1)))

	rows, err := s.db.QueryContext(ctx, `select day, metric, count from usage_counters
		where day >= ? and day <= ? order by day, metric`, windowStart, Day(now))
	if err != nil {
		return Report{}, fmt.Errorf("read usage counters: %w", err)
	}
	defer rows.Close()
	byDay := make(map[string]map[string]int64)
	for rows.Next() {
		var day, metric string
		var count int64
		if err := rows.Scan(&day, &metric, &count); err != nil {
			return Report{}, fmt.Errorf("scan usage counter row: %w", err)
		}
		if byDay[day] == nil {
			byDay[day] = make(map[string]int64)
		}
		byDay[day][metric] = count
	}
	if err := rows.Err(); err != nil {
		return Report{}, fmt.Errorf("iterate usage counter rows: %w", err)
	}

	seriesStart, err := time.Parse("2006-01-02", windowStart)
	if err != nil {
		return Report{}, fmt.Errorf("parse report window start: %w", err)
	}
	series := make([]DailyCount, 0, days)
	for i := range days {
		day := seriesStart.AddDate(0, 0, i).Format("2006-01-02")
		metrics := byDay[day]
		if metrics == nil {
			metrics = map[string]int64{}
		}
		series = append(series, DailyCount{Day: day, Metrics: metrics})
	}

	totals, err := s.totals(ctx)
	if err != nil {
		return Report{}, err
	}
	live, err := s.liveCounts(ctx, now)
	if err != nil {
		return Report{}, err
	}
	return Report{Days: days, Series: series, Totals: totals, Live: live}, nil
}

func (s *Store) totals(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `select metric, sum(count) from usage_counters group by metric`)
	if err != nil {
		return nil, fmt.Errorf("sum usage counters: %w", err)
	}
	defer rows.Close()
	totals := make(map[string]int64)
	for rows.Next() {
		var metric string
		var count int64
		if err := rows.Scan(&metric, &count); err != nil {
			return nil, fmt.Errorf("scan usage total row: %w", err)
		}
		totals[metric] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage total rows: %w", err)
	}
	return totals, nil
}

func (s *Store) liveCounts(ctx context.Context, now time.Time) (LiveCounts, error) {
	// Both tables store expires_at as varying-length RFC3339 text, so the
	// expiry comparison goes through julianday instead of text comparison
	// (same reason db.StorageObjectBytes uses it).
	const liveSQL = `select
		(select count(*) from secrets
			where state = 'active' and consumed_at is null and julianday(expires_at) > julianday(?)),
		(select count(*) from requests
			where state in ('waiting', 'uploading', 'submitted') and julianday(expires_at) > julianday(?))`
	var live LiveCounts
	if err := s.db.QueryRowContext(ctx, liveSQL, now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339)).
		Scan(&live.ActiveSecrets, &live.LiveRequests); err != nil {
		return LiveCounts{}, fmt.Errorf("count live deliveries: %w", err)
	}
	return live, nil
}
