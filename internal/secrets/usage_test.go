package secrets

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/usage"
)

func usageCount(t *testing.T, conn *sql.DB, day, metric string) int64 {
	t.Helper()
	var count int64
	err := conn.QueryRow(`select count from usage_counters where day = ? and metric = ?`, day, metric).Scan(&count)
	if err == sql.ErrNoRows {
		return 0
	}
	if err != nil {
		t.Fatalf("read usage counter %s/%s: %v", day, metric, err)
	}
	return count
}

func requireUsageCount(t *testing.T, conn *sql.DB, day, metric string, want int64) {
	t.Helper()
	if got := usageCount(t, conn, day, metric); got != want {
		t.Fatalf("usage counter %s/%s = %d, want %d", metric, day, got, want)
	}
}

// Every write point below must commit its counter in the same transaction as
// the state change: the create counters ride the create tx, bytes ride the tx
// that persists the body (inline create or large finalize), and open/failure
// counters ride the open tx the HTTP handler commits.
func TestUsageCountersFollowInlineCreateAndOpen(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	day := usage.Day(now)

	if _, err := store.Create(ctx, CreateInput{
		Kind:            KindText,
		Ciphertext:      testBody,
		AccessKDF:       testAccessKDF,
		AccessProofHash: "proof-hash",
		WrappedIdentity: testWrappedIdentity,
		SizeBytes:       testBodyLen,
		TTLSeconds:      600,
		MaxViews:        1,
	}); err != nil {
		t.Fatalf("create text secret: %v", err)
	}
	requireUsageCount(t, conn, day, usage.SecretCreatedText, 1)
	requireUsageCount(t, conn, day, usage.BytesUploaded, int64(testBodyLen))
	requireUsageCount(t, conn, day, usage.SecretCreatedFile, 0)

	encryptedFilename := *ageField(200)
	contentType := "text/plain"
	if _, err := store.Create(ctx, CreateInput{
		Kind:              KindFile,
		Ciphertext:        testBody,
		AccessKDF:         testAccessKDF,
		AccessProofHash:   "proof-hash",
		WrappedIdentity:   testWrappedIdentity,
		EncryptedFilename: &encryptedFilename,
		ContentType:       &contentType,
		SizeBytes:         testBodyLen,
		TTLSeconds:        600,
		MaxViews:          1,
	}); err != nil {
		t.Fatalf("create file secret: %v", err)
	}
	requireUsageCount(t, conn, day, usage.SecretCreatedFile, 1)
	requireUsageCount(t, conn, day, usage.BytesUploaded, 2*int64(testBodyLen))

	// A failed access proof records its counter in the tx the handler commits
	// for the failed attempt (openAndEnqueueCleanup commits on ErrInvalidAccess).
	proofed, err := store.Create(ctx, CreateInput{
		Kind:            KindText,
		Ciphertext:      testBody,
		AccessKDF:       testAccessKDF,
		AccessProofHash: "proof-hash",
		WrappedIdentity: testWrappedIdentity,
		SizeBytes:       testBodyLen,
		TTLSeconds:      600,
		MaxViews:        1,
	})
	if err != nil {
		t.Fatalf("create proofed secret: %v", err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenTx(ctx, tx, proofed.ID, "wrong-proof-hash"); !errors.Is(err, ErrInvalidAccess) {
		_ = tx.Rollback()
		t.Fatalf("wrong proof error = %v, want ErrInvalidAccess", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed access tx: %v", err)
	}
	requireUsageCount(t, conn, day, usage.SecretOpenFailed, 1)
	requireUsageCount(t, conn, day, usage.SecretOpened, 0)

	// A Model B secret carries no access KDF, proof hash, or wrapped identity:
	// the link fragment is the capability, so it opens without a proof.
	modelB, err := store.Create(ctx, CreateInput{
		Kind:       KindText,
		Ciphertext: testBody,
		SizeBytes:  testBodyLen,
		TTLSeconds: 600,
		MaxViews:   1,
	})
	if err != nil {
		t.Fatalf("create model b secret: %v", err)
	}
	tx, err = conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := store.OpenTx(ctx, tx, modelB.ID, "")
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("open model b secret: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit open tx: %v", err)
	}
	if opened.ID != modelB.ID {
		t.Fatalf("opened id = %q, want %q", opened.ID, modelB.ID)
	}
	requireUsageCount(t, conn, day, usage.SecretOpened, 1)
}

func TestUsageCountersFollowLargeCreateAndFinalize(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	mock := newMockObjectStore()
	store := newLargeTestStore(t, conn, mock)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	day := usage.Day(now)

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
	// The create counts the event but not the bytes: the object has not landed.
	requireUsageCount(t, conn, day, usage.SecretCreatedFile, 1)
	requireUsageCount(t, conn, day, usage.BytesUploaded, 0)

	// The client uploads the age file straight to the bucket; finalize confirms
	// the exact length, and only then do the bytes count.
	mock.objects["managed/secrets/"+res.ID] = ageBody(1000)
	if err := store.Finalize(ctx, res.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	requireUsageCount(t, conn, day, usage.BytesUploaded, 1000)

	// Finalize is idempotent: replaying it must not double-count the bytes.
	if err := store.Finalize(ctx, res.ID); err != nil {
		t.Fatalf("finalize replay: %v", err)
	}
	requireUsageCount(t, conn, day, usage.BytesUploaded, 1000)
}
