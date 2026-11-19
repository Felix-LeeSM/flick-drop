package db

import (
	"context"
	"database/sql"
	"time"
)

const StorageInline = "sqlite_blob"

// Mirrors requests.openObjectGrace and secrets openObjectGrace (30-min stream + 5-min margin).
const objectGrace = 35 * time.Minute

// Takes a queryer so Reserve reads usage inside its reservation transaction. Under
// READ COMMITTED concurrent placements can still overshoot: the ceiling is a soft cap.
type RowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Counts reserved, submitted, and grace-window consumed secret rows; an opened secret
// no longer counts (small under-count the 90% margin absorbs). The result has exactly
// the keys in backends, so a stray column value never reaches a metric label.
func StorageObjectBytes(ctx context.Context, conn RowQueryer, now time.Time, backends []string) (map[string]int64, error) {
	usage := make(map[string]int64, len(backends))
	const secretsSQL = `select coalesce(sum(size_bytes), 0) from secrets
		where storage_backend = $1
			and (consumed_at is null or consumed_at > $2)`
	// Consumed requests have size_bytes nulled (under-count). Uploading rows count staging
	// and final keys twice: the DB cannot tell if finalize copied, and over-count is safe.
	const requestsSQL = `select coalesce(sum((
			case when upload_key is not null then 1 else 0 end
			+ case when final_key is not null then 1 else 0 end
		) * size_bytes), 0)
		from requests
		where storage_backend = $1 and (upload_key is not null or final_key is not null)`
	grace := now.UTC().Add(-objectGrace)
	for _, backend := range backends {
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
