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
	objects       map[string][]byte
	presignedSize int64
	openErr       error
	openSizeDelta int64
	opened        []*trackedBody
}

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
		Objects:               testRegistry(objects),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

// The access proof lets a wrong proof trip the lockout, whose purge is the object delete.
func createFinalizedS3Secret(t *testing.T, ctx context.Context, store *Store, mock *mockObjectStore) string {
	t.Helper()
	res, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind:              KindFile,
		EncryptedFilename: ageField(200),
		AccessProofHash:   "proof-hash",
		AccessKDF:         testAccessKDF,
		WrappedIdentity:   testWrappedIdentity,
		SizeBytes:         1000,
		TTLSeconds:        600,
	})
	if err != nil {
		t.Fatalf("create large: %v", err)
	}
	mock.objects["managed/secrets/"+res.ID] = ageBody(1000)
	if err := store.Finalize(ctx, res.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	return res.ID
}

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
		Objects:               testRegistry(mock),
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
	if _, ok := mock.objects["managed/secrets/"+id]; !ok {
		t.Errorf("object deleted inline, want left for the enqueued worker job")
	}
}

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
	if err := conn.QueryRowContext(ctx, `select state from secrets where id = $1`, id).Scan(&state); err != nil {
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
	if err := conn.QueryRowContext(ctx, `select count(*) from secret_payloads where secret_id = $1`, res.ID).Scan(&payloadCount); err != nil {
		t.Fatalf("count payloads: %v", err)
	}
	if payloadCount != 0 {
		t.Fatalf("large secret should not write an inline payload")
	}

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
	if got.Ciphertext != nil || got.Body != nil {
		t.Fatalf("Get returned an S3 body (%d inline bytes)", len(got.Ciphertext))
	}
	if got.StorageBackend != testBackend {
		t.Fatalf("backend = %q, want %q", got.StorageBackend, testBackend)
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
	if _, err := store.Management(ctx, res.ID, res.ManagementToken); !errors.Is(err, ErrManagementUnavailable) {
		t.Fatalf("S3 management after open = %v, want unavailable", err)
	}
}

func TestFinalizeIdempotent(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock)
	store.SetNowForTest(func() time.Time { return time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC) })

	res, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind: KindFile, EncryptedFilename: ageField(200), SizeBytes: 100, TTLSeconds: 600,
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
		Kind: KindFile, EncryptedFilename: ageField(200), SizeBytes: 100, TTLSeconds: 600,
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
		Kind: KindFile, EncryptedFilename: ageField(200), SizeBytes: 100, TTLSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	mock.objects["managed/secrets/"+res.ID] = make([]byte, 5000) // exceeds the 4096 ciphertext cap
	if err := store.Finalize(ctx, res.ID); !errors.Is(err, ErrObjectMissing) {
		t.Fatalf("finalize error = %v, want ErrObjectMissing", err)
	}
}

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

	if _, err := store.activateSecretTx(ctx, tx, "sec_reaped", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("activate err = %v, want ErrNotFound (row reaped mid-finalize)", err)
	}
}

func TestActivateSecretTxRefusesReaperClaimedRow(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newLargeTestStore(t, conn, newMockObjectStore())
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)

	insertSecret(t, ctx, conn, secretFixture{
		id:             "sec_claimed",
		kind:           "file",
		storageBackend: testBackend,
		storageKey:     "obj_claimed",
		state:          "pending_upload",
		expiresAt:      now.Add(1 * time.Hour),
		createdAt:      now.Add(-1 * time.Minute),
		updatedAt:      now.Add(-1 * time.Minute),
	})
	if _, err := conn.ExecContext(ctx, `update secrets set reclaim_enqueued_at = $1 where id = 'sec_claimed'`, now); err != nil {
		t.Fatal(err)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer rollback(tx)
	if activated, err := store.activateSecretTx(ctx, tx, "sec_claimed", now); activated || !errors.Is(err, ErrNotFound) {
		t.Fatalf("activate claimed row: activated=%v err=%v, want ErrNotFound", activated, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := secretState(t, ctx, conn, "sec_claimed"); got != "pending_upload" {
		t.Fatalf("state = %q, want pending_upload", got)
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
		storageBackend: testBackend,
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
	activated, err := store.activateSecretTx(ctx, tx, "sec_pending", now)
	if err != nil || !activated {
		t.Fatalf("activate: activated=%v err=%v, want activated=true", activated, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := secretState(t, ctx, conn, "sec_pending"); got != "active" {
		t.Fatalf("state = %q, want active", got)
	}
}

func TestActivateSecretTxIsIdempotentWhenAlreadyActive(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newLargeTestStore(t, conn, newMockObjectStore())
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)

	insertSecret(t, ctx, conn, secretFixture{
		id:             "sec_activated",
		kind:           "file",
		storageBackend: testBackend,
		storageKey:     "obj_activated",
		state:          "pending_upload",
		expiresAt:      now.Add(1 * time.Hour),
		createdAt:      now.Add(-1 * time.Minute),
		updatedAt:      now.Add(-1 * time.Minute),
	})

	first, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin first tx: %v", err)
	}
	activated, err := store.activateSecretTx(ctx, first, "sec_activated", now)
	if err != nil || !activated {
		t.Fatalf("first activate: activated=%v err=%v", activated, err)
	}
	if err := first.Commit(); err != nil {
		t.Fatalf("commit first tx: %v", err)
	}

	second, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin second tx: %v", err)
	}
	defer second.Rollback()
	activated, err = store.activateSecretTx(ctx, second, "sec_activated", now)
	if err != nil || activated {
		t.Fatalf("second activate: activated=%v err=%v, want idempotent activated=false nil", activated, err)
	}
	if got := secretState(t, ctx, conn, "sec_activated"); got != "active" {
		t.Fatalf("state = %q, want still active", got)
	}
}

func TestCreateLargeSignsExactSize(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock)

	if _, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind:              KindFile,
		EncryptedFilename: ageField(200),
		SizeBytes:         1000,
		TTLSeconds:        600,
	}); err != nil {
		t.Fatalf("create large: %v", err)
	}

	if want := int64(1000); mock.presignedSize != want {
		t.Fatalf("presigned size = %d, want %d", mock.presignedSize, want)
	}
}

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
				Kind:              KindFile,
				EncryptedFilename: ageField(200),
				SizeBytes:         c.size,
				TTLSeconds:        600,
			})
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestCreateLargeRejectsOverflowingSize(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newLargeTestStore(t, conn, newMockObjectStore())

	for _, size := range []int64{math.MaxInt64} {
		_, err := store.CreateLarge(ctx, CreateLargeInput{
			Kind:              KindFile,
			EncryptedFilename: ageField(200),
			SizeBytes:         size,
			TTLSeconds:        600,
		})
		if !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatalf("size %d: err = %v, want ErrPayloadTooLarge", size, err)
		}
	}
}

func TestFinalizeRejectsWrongObjectLength(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock)

	res, err := store.CreateLarge(ctx, CreateLargeInput{
		Kind: KindFile, EncryptedFilename: ageField(200), SizeBytes: 100, TTLSeconds: 600,
	})
	if err != nil {
		t.Fatalf("create large: %v", err)
	}

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

func TestCreateLargeValidatesAgeFields(t *testing.T) {
	ctx := context.Background()
	store := newLargeTestStore(t, openTestDB(t, ctx), newMockObjectStore())
	cases := map[string]CreateLargeInput{
		"model A without wrapped identity": {Kind: KindFile, EncryptedFilename: ageField(200), AccessKDF: testAccessKDF, AccessProofHash: "proof"},
		"model B with wrapped identity":    {Kind: KindFile, EncryptedFilename: ageField(200), WrappedIdentity: testWrappedIdentity},
		"text with encrypted filename":     {Kind: KindText, EncryptedFilename: ageField(200)},
		"text on the large path":           {Kind: KindText},
		"file without encrypted filename":  {Kind: KindFile},
	}
	for name, in := range cases {
		in.SizeBytes, in.TTLSeconds = 100, 600
		if _, err := store.CreateLarge(ctx, in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}

func consumedAt(t *testing.T, conn *sql.DB, id string) sql.NullTime {
	t.Helper()
	var consumed sql.NullTime
	if err := conn.QueryRow(`select consumed_at from secrets where id = $1`, id).Scan(&consumed); err != nil {
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
		t.Fatalf("failed object open consumed the secret at %s", c.Time)
	}

	mock.openErr = nil
	opened, err := openS3(t, ctx, conn, store, id)
	if err != nil {
		t.Fatalf("reopen after bucket recovered: %v", err)
	}
	opened.Body.Close()
}

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
				t.Fatalf("length mismatch consumed the secret at %s", c.Time)
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

const (
	testBackend      = "oci"
	otherTestBackend = "r2"
)

func testRegistry(objects storage.ObjectStore) *storage.Registry {
	registry, err := storage.NewRegistry(storage.Backend{Name: testBackend, Store: objects})
	if err != nil {
		panic(err)
	}
	return registry
}

func dualLargeTestStore(t *testing.T, conn *sql.DB, testCeiling, otherCeiling int64) (*Store, *mockObjectStore, *mockObjectStore) {
	t.Helper()
	first := newMockObjectStore()
	other := newMockObjectStore()
	registry, err := storage.NewRegistry(
		storage.Backend{Name: testBackend, Store: first, MaxTotalBytes: testCeiling},
		storage.Backend{Name: otherTestBackend, Store: other, MaxTotalBytes: otherCeiling},
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(conn, StoreOptions{
		PayloadInlineMaxBytes: 1024,
		MaxObjectBytes:        4096,
		PresignTTL:            5 * time.Minute,
		PendingTTL:            15 * time.Minute,
		MinTTLSeconds:         300,
		MaxTTLSeconds:         604800,
		Objects:               registry,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store, first, other
}

func secretBackend(t *testing.T, ctx context.Context, conn *sql.DB, id string) string {
	t.Helper()
	var backend string
	if err := conn.QueryRowContext(ctx, `select storage_backend from secrets where id = $1`, id).Scan(&backend); err != nil {
		t.Fatal(err)
	}
	return backend
}

func largeCreateInput() CreateLargeInput {
	return CreateLargeInput{
		Kind:              KindFile,
		EncryptedFilename: ageField(200),
		SizeBytes:         1000,
		TTLSeconds:        600,
	}
}

// Placement is random, so each test leaves exactly one candidate.
func TestCreateLargeRoutesToTheOnlyBackendThatFits(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		testCeiling, otherCeiling int64
		want                      string
	}{
		{"first configured fits", 4096, 999, testBackend},
		{"second configured fits", 999, 4096, otherTestBackend}, // 999 is below the 1000-byte body
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			conn := openTestDB(t, ctx)
			store, first, other := dualLargeTestStore(t, conn, tc.testCeiling, tc.otherCeiling)

			res, err := store.CreateLarge(ctx, largeCreateInput())
			if err != nil {
				t.Fatalf("create large: %v", err)
			}
			if got := secretBackend(t, ctx, conn, res.ID); got != tc.want {
				t.Fatalf("storage_backend = %q, want %q, the only backend whose ceiling fits", got, tc.want)
			}
			presigned := map[string]*mockObjectStore{testBackend: first, otherTestBackend: other}[tc.want]
			if presigned.presignedSize != 1000 {
				t.Fatalf("%s presigned size = %d, want 1000", tc.want, presigned.presignedSize)
			}
		})
	}
}

func TestCreateLargeRejectsWhenEveryBackendIsFull(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store, _, _ := dualLargeTestStore(t, conn, 100, 100)

	if _, err := store.CreateLarge(ctx, largeCreateInput()); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("err = %v, want ErrStorageFull", err)
	}
	var count int
	if err := conn.QueryRowContext(ctx, `select count(*) from secrets`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("a rejection left %d secret rows behind", count)
	}
}

func TestCreateLargeSingleUncappedBackendAlwaysFits(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newLargeTestStore(t, conn, newMockObjectStore()) // MaxTotalBytes 0 = unlimited

	for range 3 {
		res, err := store.CreateLarge(ctx, largeCreateInput())
		if err != nil {
			t.Fatalf("create large: %v", err)
		}
		if got := secretBackend(t, ctx, conn, res.ID); got != testBackend {
			t.Fatalf("storage_backend = %q, want %q", got, testBackend)
		}
	}
}
