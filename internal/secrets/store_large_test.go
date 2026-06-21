package secrets

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

type mockObjectStore struct {
	objects map[string][]byte
}

func newMockObjectStore() *mockObjectStore {
	return &mockObjectStore{objects: map[string][]byte{}}
}

func (m *mockObjectStore) PresignPOST(_ context.Context, _ string, _ int64, _ time.Duration) (storage.POSTForm, error) {
	return storage.POSTForm{
		URL:       "http://localhost:9000/flick-dev",
		Method:    "POST",
		Fields:    map[string]string{"key": "k"},
		FileField: "file",
	}, nil
}

func (m *mockObjectStore) Head(_ context.Context, key string) (storage.ObjectInfo, error) {
	b, ok := m.objects[key]
	if !ok {
		return storage.ObjectInfo{Key: key}, nil
	}
	return storage.ObjectInfo{Key: key, Exists: true, Size: int64(len(b))}, nil
}

func (m *mockObjectStore) Get(_ context.Context, key string) ([]byte, error) {
	b, ok := m.objects[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return b, nil
}

func (m *mockObjectStore) Delete(_ context.Context, key string) error {
	delete(m.objects, key)
	return nil
}

func newLargeTestStore(t *testing.T, conn *sql.DB, objects storage.ObjectStore) *Store {
	t.Helper()
	store, err := NewStore(conn, StoreOptions{
		PayloadInlineMaxBytes: 1024,
		MaxObjectBytes:        4096,
		PresignTTL:            5 * time.Minute,
		PendingTTL:            15 * time.Minute,
		MinTTLSeconds:         300,
		MaxTTLSeconds:         604800,
		Objects:               objects,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func secretState(t *testing.T, ctx context.Context, conn *sql.DB, id string) string {
	t.Helper()
	var state string
	if err := conn.QueryRowContext(ctx, `select state from secrets where id = ?`, id).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	return state
}

func TestCreateLargeFinalizeGet(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	filename := "report.enc"
	res, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind:              KindFile,
		Nonce:             "nonce",
		EncryptedFilename: &filename,
		SizeBytes:         1000,
		TTLSeconds:        600,
	})
	if err != nil {
		t.Fatalf("create large: %v", err)
	}
	if res.ID == "" || res.Upload.URL == "" {
		t.Fatalf("missing id/upload: %+v", res)
	}
	if got := secretState(t, ctx, conn, res.ID); got != "pending_upload" {
		t.Fatalf("state = %q, want pending_upload", got)
	}

	var payloadCount int
	if err := conn.QueryRowContext(ctx, `select count(*) from secret_payloads where secret_id = ?`, res.ID).Scan(&payloadCount); err != nil {
		t.Fatalf("count payloads: %v", err)
	}
	if payloadCount != 0 {
		t.Fatalf("large secret should not write an inline payload")
	}

	// client uploads ciphertext straight to the bucket.
	mock.objects[res.ID] = []byte("ciphertext-bytes")

	if err := store.Finalize(ctx, res.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if got := secretState(t, ctx, conn, res.ID); got != "active" {
		t.Fatalf("state = %q, want active", got)
	}

	got, err := store.Get(ctx, res.ID)
	if err != nil {
		t.Fatalf("get large secret: %v", err)
	}
	if string(got.Ciphertext) != "ciphertext-bytes" {
		t.Fatalf("ciphertext = %q", string(got.Ciphertext))
	}
	if got.StorageBackend != StorageS3 {
		t.Fatalf("backend = %q, want s3_object", got.StorageBackend)
	}
}

func TestFinalizeIdempotent(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock)
	store.SetNowForTest(func() time.Time { return time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC) })

	res, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind: KindText, Nonce: "n", SizeBytes: 10, TTLSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	mock.objects[res.ID] = []byte("ct")

	if err := store.Finalize(ctx, res.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := store.Finalize(ctx, res.ID); err != nil {
		t.Fatalf("second finalize (idempotent): %v", err)
	}
}

func TestFinalizeMissingObject(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock)
	store.SetNowForTest(func() time.Time { return time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC) })

	res, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind: KindText, Nonce: "n", SizeBytes: 10, TTLSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(ctx, res.ID); !errors.Is(err, ErrObjectMissing) {
		t.Fatalf("finalize error = %v, want ErrObjectMissing", err)
	}
	if got := secretState(t, ctx, conn, res.ID); got != "pending_upload" {
		t.Fatalf("state = %q, want still pending", got)
	}
}

func TestFinalizeOversized(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock) // maxObjectBytes 4096
	store.SetNowForTest(func() time.Time { return time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC) })

	res, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind: KindText, Nonce: "n", SizeBytes: 10, TTLSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	mock.objects[res.ID] = make([]byte, 5000) // exceeds the 4096 ciphertext cap
	if err := store.Finalize(ctx, res.ID); !errors.Is(err, ErrObjectMissing) {
		t.Fatalf("finalize error = %v, want ErrObjectMissing", err)
	}
}

// TestActivateSecretTxReturnsNotFoundWhenRowGone covers the reaper hard-delete
// race: if a pending_upload row is removed between Finalize's SELECT and its
// UPDATE, the UPDATE matches 0 rows and the guard must surface ErrNotFound
// rather than a silent success. The race can't be reproduced through SQLite
// concurrency (a read txn blocks a concurrent write with SQLITE_BUSY_SNAPSHOT),
// so the guard is exercised directly on a transaction with no matching row.
func TestActivateSecretTxReturnsNotFoundWhenRowGone(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newLargeTestStore(t, conn, newMockObjectStore())
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer rollback(tx)

	if err := store.activateSecretTx(ctx, tx, "sec_reaped", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("activate err = %v, want ErrNotFound (row reaped mid-finalize)", err)
	}
}

func TestActivateSecretTxFlipsPendingToActive(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newLargeTestStore(t, conn, newMockObjectStore())
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)

	insertSecret(t, ctx, conn, secretFixture{
		id:             "sec_pending",
		kind:           "file",
		storageBackend: "s3_object",
		storageKey:     "obj_pending",
		state:          "pending_upload",
		expiresAt:      now.Add(1 * time.Hour),
		createdAt:      now.Add(-1 * time.Minute),
		updatedAt:      now.Add(-1 * time.Minute),
	})

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := store.activateSecretTx(ctx, tx, "sec_pending", now); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := secretState(t, ctx, conn, "sec_pending"); got != "active" {
		t.Fatalf("state = %q, want active", got)
	}
}
