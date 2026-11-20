package db

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

const (
	s3Backend     = "oci"
	r2Backend     = "r2"
	sqliteBackend = StorageInline
)

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
		var consumedAt any
		if r.consumedAt != "" {
			parsed, err := time.Parse(time.RFC3339Nano, r.consumedAt)
			if err != nil {
				t.Fatalf("parse consumed_at %q: %v", r.consumedAt, err)
			}
			consumedAt = parsed
		}
		_, err := conn.Exec(`insert into secrets
			(id, kind, storage_backend, storage_key, size_bytes, max_views, view_count, state, expires_at, consumed_at, created_at, updated_at)
			values ($1, 'text', $2, $3, $4, 1, 0, $5, $6, $7, $8, $9)`,
			"sec-"+r.backend+"-"+string(rune('a'+i)), r.backend, "key-"+r.backend, r.size, r.state,
			time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), consumedAt,
			time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
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
			values ($1, $2, 'fp', $3, $4, $5, 'file', $6, '{}', $7, $8, $9, $10, $11, $12, $13, $14)`,
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
			time.Date(2026, 11, 9, 12, 10, 0, 0, time.UTC),
			time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("insert request %d (%s, %s): %v", i, r.backend, r.state, err)
		}
	}
}

func TestStorageObjectBytesPerBackend(t *testing.T) {
	ctx := context.Background()
	conn := openTestAPIDatabase(t)

	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

	insertUsageSecrets(t, conn, []usageSecret{
		{s3Backend, 100, "active", ""},
		// Consumed within the 35-minute open grace still counts.
		{s3Backend, 200, "active", "2026-11-09T11:50:00.123456789Z"},
		{s3Backend, 400, "active", "2026-11-09T11:20:00Z"},
		{s3Backend, 80, "pending_upload", ""},
		{r2Backend, 50, "active", ""},
		{sqliteBackend, 999, "active", ""},
	})

	insertUsageRequests(t, conn, []usageRequest{
		// Uploading rows count twice (staging + possible final): the safe over-count.
		{s3Backend, "uploading", 10, "managed/requests/a/upload", "managed/requests/a/final"},
		{s3Backend, "submitted", 20, "", "managed/requests/b/final"},
		{r2Backend, "submitted", 30, "", "managed/requests/c/final"},
		{s3Backend, "waiting", 0, "", ""},
		{sqliteBackend, "waiting", 0, "", ""},
	})

	usage, err := StorageObjectBytes(ctx, conn, now, []string{s3Backend, r2Backend, "unused"})
	if err != nil {
		t.Fatalf("StorageObjectBytes: %v", err)
	}
	if got, ok := usage["unused"]; !ok || got != 0 || len(usage) != 3 {
		t.Fatalf("usage = %v, want exactly the three configured names", usage)
	}
	// Secrets: 100 + 200 (grace) + 80 (pending) = 380.
	// Requests: 10*2 (uploading, both keys) + 20 (submitted) = 40.
	if got, want := usage[s3Backend], int64(420); got != want {
		t.Fatalf("oci usage = %d, want %d", got, want)
	}
	// Secrets: 50. Requests: 30.
	if got, want := usage[r2Backend], int64(80); got != want {
		t.Fatalf("r2 usage = %d, want %d", got, want)
	}
}
