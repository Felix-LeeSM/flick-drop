package db

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// Test-local aliases for the storage_backend column values.
const (
	s3Backend     = "s3_object"
	r2Backend     = "r2_object"
	sqliteBackend = "sqlite_blob"
)

// seedUsageRows inserts one row per scenario. Backend is the storage_backend
// column value; size is size_bytes in bytes.
type usageSecret struct {
	backend    string
	size       int64
	state      string
	consumedAt string // RFC3339Nano, empty = never consumed
}

type usageRequest struct {
	backend   string
	state     string
	size      int64 // 0 leaves size_bytes null
	uploadKey string
	finalKey  string
}

func insertUsageSecrets(t *testing.T, conn *sql.DB, rows []usageSecret) {
	t.Helper()
	for i, r := range rows {
		consumed := "null"
		if r.consumedAt != "" {
			consumed = "'" + r.consumedAt + "'"
		}
		_, err := conn.Exec(`insert into secrets
			(id, kind, storage_backend, storage_key, size_bytes, max_views, view_count, state, expires_at, consumed_at, created_at, updated_at)
			values (?, 'text', ?, ?, ?, 1, 0, ?, '2026-12-01T00:00:00Z', `+consumed+`, '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')`,
			"sec-"+r.backend+"-"+string(rune('a'+i)), r.backend, "key-"+r.backend, r.size, r.state)
		if err != nil {
			t.Fatalf("insert secret %d (%s): %v", i, r.backend, err)
		}
	}
}

func insertUsageRequests(t *testing.T, conn *sql.DB, rows []usageRequest) {
	t.Helper()
	for i, r := range rows {
		id := "req-" + r.backend + "-" + string(rune('a'+i))
		var size any
		if r.size > 0 {
			size = r.size
		}
		var upload, final any
		if r.uploadKey != "" {
			upload = r.uploadKey
		}
		if r.finalKey != "" {
			final = r.finalKey
		}
		_, err := conn.Exec(`insert into requests
			(id, public_key, fingerprint, submission_token_hash, retrieval_token_hash, state, kind, size_bytes, envelope_json, storage_backend, upload_key, final_key, ciphertext_sha256, attempt_token_hash, attempt_body_hash, reservation_expires_at, expires_at)
			values (?, ?, 'fp', ?, ?, ?, 'file', ?, '{}', ?, ?, ?, ?, ?, ?, ?, '2026-12-01T00:00:00Z')`,
			id,
			"pk-"+id,
			[]byte{
				1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
				1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
			},
			[]byte{
				2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
				2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
			},
			r.state, size, r.backend, upload, final,
			[]byte{
				3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3,
				3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3,
			},
			[]byte{
				4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4,
				4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4,
			},
			[]byte{
				5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5,
				5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5,
			},
			"2026-11-09T12:10:00Z")
		if err != nil {
			t.Fatalf("insert request %d (%s, %s): %v", i, r.backend, r.state, err)
		}
	}
}

func TestStorageObjectBytesPerBackend(t *testing.T) {
	ctx := context.Background()
	conn, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

	insertUsageSecrets(t, conn, []usageSecret{
		// Active objects count in full.
		{s3Backend, 100, "active", ""},
		// Consumed within the 35-minute open grace still counts: the bucket
		// object may outlive the row's consumed flag.
		{s3Backend, 200, "active", "2026-11-09T11:50:00.123456789Z"},
		// Consumed past the grace no longer counts.
		{s3Backend, 400, "active", "2026-11-09T11:20:00Z"},
		// Staged pending_upload secrets count (the staging object exists).
		{s3Backend, 80, "pending_upload", ""},
		// The secondary backend aggregates separately.
		{r2Backend, 50, "active", ""},
		// Inline blobs never live in a bucket and never count.
		{sqliteBackend, 999, "active", ""},
	})

	insertUsageRequests(t, conn, []usageRequest{
		// Uploading rows carry both keys and count twice (staging + possible
		// final object): the safe over-count direction.
		{s3Backend, "uploading", 10, "managed/requests/a/upload", "managed/requests/a/final"},
		// Submitted rows hold one final object.
		{s3Backend, "submitted", 20, "", "managed/requests/b/final"},
		// The secondary backend aggregates separately.
		{r2Backend, "submitted", 30, "", "managed/requests/c/final"},
		// Waiting and inline rows carry no object keys and contribute nothing.
		{s3Backend, "waiting", 0, "", ""},
		{sqliteBackend, "waiting", 0, "", ""},
	})

	usage, err := StorageObjectBytes(ctx, conn, now)
	if err != nil {
		t.Fatalf("StorageObjectBytes: %v", err)
	}
	// Secrets: 100 + 200 (grace) + 80 (pending) = 380.
	// Requests: 10*2 (uploading, both keys) + 20 (submitted) = 40.
	if got, want := usage[s3Backend], int64(420); got != want {
		t.Fatalf("s3_object usage = %d, want %d", got, want)
	}
	// Secrets: 50. Requests: 30.
	if got, want := usage[r2Backend], int64(80); got != want {
		t.Fatalf("r2_object usage = %d, want %d", got, want)
	}
}
