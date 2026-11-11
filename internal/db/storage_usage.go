package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

// objectGrace mirrors the open-stream grace both domains keep
// (requests.openObjectGrace, secrets openObjectGrace: a 30-minute stream plus
// a 5-minute margin). Objects consumed inside the grace still exist in the
// bucket, so counting them keeps the aggregate an over-count, which is the
// safe direction for a ceiling check. Both constants are unexported in their
// domains, so the value is pinned here with the comment pointing at them.
const objectGrace = 35 * time.Minute

// RowQueryer is the subset of *sql.DB/*sql.Tx the aggregate needs. It takes a
// queryer rather than *sql.DB because Reserve reads usage inside its open
// reservation transaction: the SQLite pool is a single connection
// (db/sqlite.go SetMaxOpenConns(1)), so an out-of-band read would wait on the
// transaction's own connection and deadlock.
type RowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// StorageObjectBytes sums the size_bytes of rows whose objects are plausibly
// present in each configured bucket: reserved rows (staging object), submitted
// rows (final object), and secrets consumed (locked) within the open grace.
// An opened secret's row is deleted in the open transaction and its
// secret_open_grace record carries no size, so a body still streaming after
// open no longer counts: the same small under-count as consumed requests,
// absorbed by the 90% ceiling margin. Deletion is asynchronous, so the true
// bucket usage can differ from this sum by a couple of objects in either
// direction. The read-only aggregation stays documented in
// internal/db/AGENTS.md as a boundary exception.
func StorageObjectBytes(ctx context.Context, conn RowQueryer, now time.Time) (map[string]int64, error) {
	usage := map[string]int64{
		storage.PrimaryBackend:   0,
		storage.SecondaryBackend: 0,
	}
	// secrets.consumed_at is written with RFC3339Nano (varying length), so the
	// grace comparison goes through julianday instead of text comparison.
	const secretsSQL = `select coalesce(sum(size_bytes), 0) from secrets
		where storage_backend = ?
			and (consumed_at is null or julianday(consumed_at) > julianday(?))`
	// requests.size_bytes is nulled once an object is consumed; the grace
	// window for consumed requests therefore contributes nothing, a small
	// under-count the 90% ceiling margin absorbs. Uploading rows carry both
	// keys (staging + final), so they count twice: the DB cannot tell whether
	// finalize has already copied the final object into the bucket, and the
	// double count is the over-count direction, which the ceiling check needs.
	const requestsSQL = `select coalesce(sum(((upload_key is not null) + (final_key is not null)) * size_bytes), 0)
		from requests
		where storage_backend = ? and (upload_key is not null or final_key is not null)`
	grace := now.UTC().Add(-objectGrace).Format(time.RFC3339)
	for _, backend := range []string{storage.PrimaryBackend, storage.SecondaryBackend} {
		var total int64
		if err := conn.QueryRowContext(ctx, secretsSQL, backend, grace).Scan(&total); err != nil {
			return nil, err
		}
		usage[backend] += total
		if err := conn.QueryRowContext(ctx, requestsSQL, backend).Scan(&total); err != nil {
			return nil, err
		}
		usage[backend] += total
	}
	return usage, nil
}
