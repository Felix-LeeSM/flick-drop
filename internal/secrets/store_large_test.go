package secrets

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

type mockObjectStore struct {
	objects map[string][]byte
	// presignedSize records the length the store asked to be signed, so tests can
	// assert it is exactly the declared age file length.
	presignedSize int64
	// openErr fails Open; openSizeDelta skews the length Open reports.
	openErr       error
	openSizeDelta int64
	opened        []*trackedBody
}

// trackedBody records whether the store closed a body it was handed.
type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func newMockObjectStore() *mockObjectStore {
	return &mockObjectStore{objects: map[string][]byte{}}
}

func (m *mockObjectStore) List(_ context.Context, prefix, cursor string, limit int) (storage.ObjectPage, error) {
	keys := []string{}
	for key := range m.objects {
		if strings.HasPrefix(key, prefix) && key > cursor {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	page := storage.ObjectPage{}
	if len(keys) > limit {
		page.NextCursor = keys[limit-1]
		keys = keys[:limit]
	}
	page.Keys = keys
	return page, nil
}

func (m *mockObjectStore) PresignPUT(_ context.Context, key string, size int64, _ time.Duration) (storage.UploadInstruction, error) {
	m.presignedSize = size
	return storage.UploadInstruction{
		URL:     "http://localhost:9000/flick-dev/" + key,
		Method:  "PUT",
		Headers: map[string]string{"Content-Length": strconv.FormatInt(size, 10)},
	}, nil
}

func (m *mockObjectStore) Head(_ context.Context, key string) (storage.ObjectInfo, error) {
	b, ok := m.objects[key]
	if !ok {
		return storage.ObjectInfo{Key: key}, nil
	}
	return storage.ObjectInfo{Key: key, Exists: true, Size: int64(len(b))}, nil
}

func (m *mockObjectStore) Open(_ context.Context, key string) (io.ReadCloser, int64, error) {
	if m.openErr != nil {
		return nil, 0, m.openErr
	}
	b, ok := m.objects[key]
	if !ok {
		return nil, 0, errors.New("not found")
	}
	body := &trackedBody{Reader: bytes.NewReader(b)}
	m.opened = append(m.opened, body)
	return body, int64(len(b)) + m.openSizeDelta, nil
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

// createFinalizedS3Secret makes an active S3-backed Model A secret. The access
// proof means a wrong proof on open trips the failed-access lockout path, whose
// payload purge is the object delete this file exercises.
func createFinalizedS3Secret(t *testing.T, ctx context.Context, store *Store, mock *mockObjectStore) string {
	t.Helper()
	res, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind:            KindText,
		AccessProofHash: "proof-hash",
		AccessKDF:       testAccessKDF,
		WrappedIdentity: testWrappedIdentity,
		SizeBytes:       1000,
		TTLSeconds:      600,
	})
	if err != nil {
		t.Fatalf("create large: %v", err)
	}
	// Finalize checks the exact staged length.
	mock.objects["managed/secrets/"+res.ID] = ageBody(1000)
	if err := store.Finalize(ctx, res.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	return res.ID
}

// driveToLockout runs the wrong-proof opens that trip the lockout on the final
// attempt, committing each like the real handler does on ErrInvalidAccess.
func driveToLockout(t *testing.T, ctx context.Context, conn *sql.DB, store *Store, id string) {
	t.Helper()
	for attempt := 1; attempt <= maxFailedAccessAttempts; attempt++ {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin tx %d: %v", attempt, err)
		}
		if _, err := store.OpenTx(ctx, tx, id, "wrong-proof-hash"); !errors.Is(err, ErrInvalidAccess) {
			t.Fatalf("attempt %d err = %v, want ErrInvalidAccess", attempt, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit tx %d: %v", attempt, err)
		}
	}
}

// With an outbox wired, the lockout purge enqueues a KindDeleteOCIObject job in
// the access tx instead of calling the object store inline — so a transient S3
// failure can't both pin the single DB connection and silently leak the object.
func TestStoreOpenTxLockoutEnqueuesObjectDeleteWithOutbox(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	outbox := &fakeOutbox{}
	store, err := NewStore(conn, StoreOptions{
		PayloadInlineMaxBytes: 1024,
		MaxObjectBytes:        4096,
		MinTTLSeconds:         300,
		MaxTTLSeconds:         604800,
		Objects:               mock,
		Outbox:                outbox,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	id := createFinalizedS3Secret(t, ctx, store, mock)
	driveToLockout(t, ctx, conn, store, id)

	if len(outbox.enqueued) != 1 {
		t.Fatalf("enqueued jobs = %d, want 1 object-delete", len(outbox.enqueued))
	}
	job := outbox.enqueued[0]
	if job.Kind != events.KindDeleteOCIObject {
		t.Errorf("job kind = %q, want %q", job.Kind, events.KindDeleteOCIObject)
	}
	if job.ObjectKey != "managed/secrets/"+id {
		t.Errorf("job object key = %q, want %q", job.ObjectKey, id)
	}
	if job.Reason != events.ReasonConsumed {
		t.Errorf("job reason = %q, want %q", job.Reason, events.ReasonConsumed)
	}
	// The delete is the worker's job; nothing touched the bucket inline.
	if _, ok := mock.objects["managed/secrets/"+id]; !ok {
		t.Errorf("object deleted inline, want left for the enqueued worker job")
	}
}

// Without an outbox (test-only Store), the lockout falls back to a best-effort
// inline delete so the locked payload is still purged.
func TestStoreOpenTxLockoutDeletesObjectInlineWithoutOutbox(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock) // no outbox wired
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	id := createFinalizedS3Secret(t, ctx, store, mock)
	driveToLockout(t, ctx, conn, store, id)

	if _, ok := mock.objects["managed/secrets/"+id]; ok {
		t.Errorf("object survived lockout, want best-effort inline delete")
	}
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

	res, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind:              KindFile,
		EncryptedFilename: ageField(200),
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

	// client uploads the age file straight to the bucket. Finalize verifies the
	// exact staged length.
	ciphertext := ageBody(1000)
	mock.objects["managed/secrets/"+res.ID] = ciphertext

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
	// Get reads metadata only; the bucket body is streamed by OpenTx.
	if got.Ciphertext != nil || got.Body != nil {
		t.Fatalf("Get returned an S3 body (%d inline bytes)", len(got.Ciphertext))
	}
	if got.StorageBackend != StorageS3 {
		t.Fatalf("backend = %q, want s3_object", got.StorageBackend)
	}
	if status, err := store.Management(ctx, res.ID, res.ManagementToken); err != nil || status.Status != "active" {
		t.Fatalf("finalized management: %+v %v", status, err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	opened, err := store.OpenTx(ctx, tx, res.ID, "")
	if err != nil || opened.ManagementToken != "" {
		t.Fatalf("open leaked capability or failed: %v", err)
	}
	if opened.Ciphertext != nil || opened.Body == nil {
		t.Fatalf("S3 open: inline %d bytes, body %v; want a streamed body only", len(opened.Ciphertext), opened.Body)
	}
	streamed, err := io.ReadAll(opened.Body)
	if err != nil || !bytes.Equal(streamed, ciphertext) {
		t.Fatalf("streamed body mismatch (%d bytes): %v", len(streamed), err)
	}
	opened.Body.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(ctx, res.ID); err != nil {
		t.Fatal(err)
	}
	if status, err := store.Management(ctx, res.ID, res.ManagementToken); err != nil || status.Status != "opened" {
		t.Fatalf("S3 outcome after cleanup: %+v %v", status, err)
	}
}

func TestFinalizeIdempotent(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock)
	store.SetNowForTest(func() time.Time { return time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC) })

	res, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind: KindText, SizeBytes: 100, TTLSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	mock.objects["managed/secrets/"+res.ID] = ageBody(100)

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
		Kind: KindText, SizeBytes: 100, TTLSeconds: 600,
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
		Kind: KindText, SizeBytes: 100, TTLSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	mock.objects["managed/secrets/"+res.ID] = make([]byte, 5000) // exceeds the 4096 ciphertext cap
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

// The presigned upload pins Content-Length, and size_bytes is the exact age
// file length, so the store must sign exactly the declared size.
func TestCreateLargeSignsExactSize(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock)

	if _, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind:       KindText,
		SizeBytes:  1000,
		TTLSeconds: 600,
	}); err != nil {
		t.Fatalf("create large: %v", err)
	}

	if want := int64(1000); mock.presignedSize != want {
		t.Fatalf("presigned size = %d, want %d", mock.presignedSize, want)
	}
}

// A declared size past the object cap must be refused
// before signing — otherwise the client gets a signature for an upload the
// bucket is meant to never accept.
func TestCreateLargeRejectsSizePastTheObjectCap(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock) // MaxObjectBytes: 4096

	cases := []struct {
		name    string
		size    int64
		wantErr error
	}{
		{"exactly at the cap", 4096, nil},
		{"one byte past the cap", 4097, ErrPayloadTooLarge},
		// The smallest age file is a payload nonce and one tag past the header.
		{"smallest age file", MaxCiphertextBytes(0) - AgeHeaderAllowanceBytes, nil},
		{"shorter than any age file", MaxCiphertextBytes(0) - AgeHeaderAllowanceBytes - 1, ErrInvalidInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := store.CreateLarge(ctx, CreateLargeInput{
				Kind:       KindText,
				SizeBytes:  c.size,
				TTLSeconds: 600,
			})
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
		})
	}
}

// SizeBytes is attacker-controlled; the largest value must be refused as too
// large, not reach the presigner and surface as a 500.
func TestCreateLargeRejectsOverflowingSize(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newLargeTestStore(t, conn, newMockObjectStore())

	for _, size := range []int64{math.MaxInt64} {
		_, err := store.CreateLarge(ctx, CreateLargeInput{
			Kind:       KindText,
			SizeBytes:  size,
			TTLSeconds: 600,
		})
		if !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatalf("size %d: err = %v, want ErrPayloadTooLarge", size, err)
		}
	}
}

// Finalize must not activate an object whose length differs from what was
// staged, even though the bucket signature should already have refused it.
func TestFinalizeRejectsWrongObjectLength(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock)

	res, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind: KindText, SizeBytes: 100, TTLSeconds: 600,
	})
	if err != nil {
		t.Fatalf("create large: %v", err)
	}

	// Within the object cap, but not the length this secret was staged for.
	for _, n := range []int{99, 101} {
		mock.objects["managed/secrets/"+res.ID] = ageBody(n)
		if err := store.Finalize(ctx, res.ID); !errors.Is(err, ErrObjectMissing) {
			t.Fatalf("finalize %d-byte object err = %v, want ErrObjectMissing", n, err)
		}
	}
	if got := secretState(t, ctx, conn, res.ID); got != "pending_upload" {
		t.Fatalf("state = %q, want pending_upload (not activated)", got)
	}
}

// CreateLarge shares the age field rules with Create: Model A carries a
// wrapped identity, Model B does not, and only files carry a filename.
func TestCreateLargeValidatesAgeFields(t *testing.T) {
	ctx := context.Background()
	store := newLargeTestStore(t, openTestDB(t, ctx), newMockObjectStore())
	cases := map[string]CreateLargeInput{
		"model A without wrapped identity": {Kind: KindText, AccessKDF: testAccessKDF, AccessProofHash: "proof"},
		"model B with wrapped identity":    {Kind: KindText, WrappedIdentity: testWrappedIdentity},
		"text with encrypted filename":     {Kind: KindText, EncryptedFilename: ageField(200)},
		"file without encrypted filename":  {Kind: KindFile},
	}
	for name, in := range cases {
		in.SizeBytes, in.TTLSeconds = 100, 600
		if _, err := store.CreateLarge(ctx, in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}

func consumedAt(t *testing.T, conn *sql.DB, id string) sql.NullString {
	t.Helper()
	var consumed sql.NullString
	if err := conn.QueryRow(`select consumed_at from secrets where id = ?`, id).Scan(&consumed); err != nil {
		t.Fatalf("read consumed_at: %v", err)
	}
	return consumed
}

func openS3(t *testing.T, ctx context.Context, conn *sql.DB, store *Store, id string) (Secret, error) {
	t.Helper()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := store.OpenTx(ctx, tx, id, "proof-hash")
	if err != nil {
		_ = tx.Rollback()
		return Secret{}, err
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return opened, nil
}

// A bucket that cannot start serving the body must leave the secret
// reopenable: OpenTx opens the object before it marks the secret consumed.
func TestStoreOpenTxS3OpenFailureLeavesSecretOpenable(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock)
	store.SetNowForTest(func() time.Time { return time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC) })
	id := createFinalizedS3Secret(t, ctx, store, mock)

	mock.openErr = errors.New("bucket unavailable")
	if _, err := openS3(t, ctx, conn, store, id); err == nil {
		t.Fatal("open succeeded although the bucket failed")
	}
	if c := consumedAt(t, conn, id); c.Valid {
		t.Fatalf("failed object open consumed the secret at %s", c.String)
	}

	mock.openErr = nil
	opened, err := openS3(t, ctx, conn, store, id)
	if err != nil {
		t.Fatalf("reopen after bucket recovered: %v", err)
	}
	opened.Body.Close()
}

// The handler announces size_bytes as Content-Length before streaming, so an
// object of any other length is refused, its body closed, and the secret kept.
func TestStoreOpenTxS3RefusesLengthMismatch(t *testing.T) {
	for _, delta := range []int64{-1, 1} {
		t.Run(strconv.FormatInt(delta, 10), func(t *testing.T) {
			ctx := context.Background()
			conn := openTestDB(t, ctx)
			mock := newMockObjectStore()
			store := newLargeTestStore(t, conn, mock)
			store.SetNowForTest(func() time.Time { return time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC) })
			id := createFinalizedS3Secret(t, ctx, store, mock)

			mock.openSizeDelta = delta
			if _, err := openS3(t, ctx, conn, store, id); err == nil {
				t.Fatal("open accepted an object of the wrong length")
			}
			if len(mock.opened) != 1 || !mock.opened[0].closed {
				t.Fatalf("refused body not closed (opened %d)", len(mock.opened))
			}
			if c := consumedAt(t, conn, id); c.Valid {
				t.Fatalf("length mismatch consumed the secret at %s", c.String)
			}

			mock.openSizeDelta = 0
			opened, err := openS3(t, ctx, conn, store, id)
			if err != nil {
				t.Fatalf("reopen with the right length: %v", err)
			}
			opened.Body.Close()
		})
	}
}
