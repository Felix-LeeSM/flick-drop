package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

func TestManagementOutcomesAndBoundedRetention(t *testing.T) {
	for _, outcome := range []string{"opened", "locked"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			conn := openTestDB(t, ctx)
			store := newTestStore(t, conn)
			now := time.Date(2026, 10, 8, 1, 0, 0, 123, time.UTC)
			store.SetNowForTest(func() time.Time { return now })
			input := CreateInput{Kind: KindText, Ciphertext: []byte("encrypted"), Nonce: "nonce", SizeBytes: 9, TTLSeconds: 600}
			if outcome == "locked" {
				input.AccessProofHash = "proof"
				input.KDF = KDFParams{Algorithm: KDFPBKDF2SHA256, Salt: "salt", Iterations: 600000, KeyLengthBits: 256}
				input.AccessKDF = input.KDF
				input.AccessKDF.Salt = "access-salt"
			}
			created, err := store.Create(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := base64.RawURLEncoding.DecodeString(created.ManagementToken)
			if err != nil || len(raw) != 32 {
				t.Fatal("invalid issued capability")
			}
			var stored []byte
			if err := conn.QueryRow(`select token_hash from secret_management where secret_id = ?`, created.ID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			expected := sha256.Sum256(raw)
			if !bytes.Equal(stored, expected[:]) {
				t.Fatal("capability is not hash-only")
			}
			if status, err := store.Management(ctx, created.ID, created.ManagementToken); err != nil || status.Status != "active" || !status.CanCancel {
				t.Fatalf("active: %+v, %v", status, err)
			}
			attempts := 1
			if outcome == "locked" {
				attempts = 5
			}
			for i := 0; i < attempts; i++ {
				tx, err := conn.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				_, err = store.OpenTx(ctx, tx, created.ID, "wrong-proof")
				if (outcome == "opened" && err != nil) || (outcome == "locked" && !errors.Is(err, ErrInvalidAccess)) {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Cleanup(ctx, created.ID); err != nil {
				t.Fatal(err)
			}
			if status, err := store.Management(ctx, created.ID, created.ManagementToken); err != nil || status.Status != outcome || status.CanCancel {
				t.Fatalf("cleaned: %+v, %v", status, err)
			}
			// Capability reads remain valid until the exact original deadline.
			now = created.ExpiresAt.Add(-time.Nanosecond)
			if _, err := store.Management(ctx, created.ID, created.ManagementToken); err != nil {
				t.Fatal(err)
			}
			reaper := newTestReaper(t, conn, store, newTestOutbox(t, conn), 1)
			reaper.SetNowForTest(func() time.Time { return now })
			if _, err := reaper.ClaimOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Management(ctx, created.ID, created.ManagementToken); err != nil {
				t.Fatal("purged early", err)
			}
			now = created.ExpiresAt
			if _, err := store.Management(ctx, created.ID, created.ManagementToken); !errors.Is(err, ErrManagementUnavailable) {
				t.Fatal(err)
			}
			if _, err := reaper.ClaimOnce(ctx); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"secrets", "secret_management", "secret_payloads"} {
				var count int
				if err := conn.QueryRow("select count(*) from " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s retained rows: %d, %v", table, count, err)
				}
			}
		})
	}
}

func TestManagementCapabilitiesAndTransactionRollback(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	input := CreateInput{Kind: KindText, Ciphertext: []byte("encrypted"), Nonce: "nonce", SizeBytes: 9, TTLSeconds: 600}
	first, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ManagementToken == second.ManagementToken {
		t.Fatal("reused capability")
	}
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, first.ManagementToken[42])
	noncanonical := first.ManagementToken[:42] + string(alphabet[last+1])
	for _, token := range []string{"", "bad", first.ManagementToken + "=", first.ManagementToken + "\n", noncanonical, second.ManagementToken} {
		if _, err := store.Management(ctx, first.ID, token); !errors.Is(err, ErrManagementUnavailable) {
			t.Fatalf("invalid capability accepted: %v", err)
		}
	}
	if _, err := store.Management(ctx, "missing", first.ManagementToken); !errors.Is(err, ErrManagementUnavailable) {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`create trigger reject_management before insert on secret_management begin select raise(abort, 'test failure'); end`); err != nil {
		t.Fatal(err)
	}
	if created, err := store.Create(ctx, input); err == nil || created.ManagementToken != "" {
		t.Fatal("failed creation issued a capability")
	}
	var count int
	if err := conn.QueryRow(`select count(*) from secrets`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("create did not roll back: %d %v", count, err)
	}
	if _, err := conn.Exec(`create trigger reject_outcome before update on secret_management begin select raise(abort, 'test failure'); end`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenTx(ctx, tx, first.ID, ""); err == nil {
		t.Fatal("open succeeded without its outcome")
	}
	_ = tx.Rollback()
	if _, err := store.Get(ctx, first.ID); err != nil {
		t.Fatalf("failed outcome consumed payload: %v", err)
	}
	// A legacy row without a management record keeps its old recipient flow.
	if _, err := conn.Exec(`delete from secret_management where secret_id = ?`, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Management(ctx, second.ID, second.ManagementToken); !errors.Is(err, ErrManagementUnavailable) {
		t.Fatal(err)
	}
	tx, err = conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenTx(ctx, tx, second.ID, ""); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

type clockAdvancingObjects struct {
	storage.ObjectStore
	afterIO func()
}

func (s clockAdvancingObjects) Head(ctx context.Context, key string) (storage.ObjectInfo, error) {
	info, err := s.ObjectStore.Head(ctx, key)
	s.afterIO()
	return info, err
}

func (s clockAdvancingObjects) Get(ctx context.Context, key string) ([]byte, error) {
	body, err := s.ObjectStore.Get(ctx, key)
	s.afterIO()
	return body, err
}

func TestManagementOpenCannotOutliveExpiryDuringObjectRead(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	objects := newMockObjectStore()
	store := newLargeTestStore(t, conn, objects)
	now := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	id := createFinalizedS3Secret(t, ctx, store, objects)
	deadline := now.Add(600 * time.Second)
	now = deadline.Add(-time.Second)
	store.objects = clockAdvancingObjects{objects, func() { now = deadline }}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenTx(ctx, tx, id, "proof-hash"); !errors.Is(err, ErrExpired) {
		_ = tx.Rollback()
		t.Fatalf("expired GET was released: %v", err)
	}
	_ = tx.Rollback()
	var count int
	if err := conn.QueryRow(`select count(*) from secret_management where outcome is not null`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed GET recorded a terminal outcome: %d %v", count, err)
	}
}

func TestManagementPendingUploadDeadlineAndReclaim(t *testing.T) {
	for _, ttl := range []int{600, 3600} {
		t.Run((time.Duration(ttl) * time.Second).String(), func(t *testing.T) {
			ctx := context.Background()
			conn := openTestDB(t, ctx)
			objects := newMockObjectStore()
			store := newLargeTestStore(t, conn, objects)
			now := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
			store.SetNowForTest(func() time.Time { return now })
			filename := "encrypted-name"
			created, err := store.CreateLarge(ctx, CreateLargeInput{Kind: KindFile, EncryptedFilename: &filename, Nonce: "nonce", SizeBytes: 2048, TTLSeconds: ttl})
			if err != nil {
				t.Fatal(err)
			}
			key := "managed/secrets/" + created.ID
			if !strings.HasSuffix(created.Upload.URL, key) {
				t.Fatal("upload escaped sender prefix")
			}
			objects.objects[key] = make([]byte, 2048+AEADOverheadBytes)
			if status, err := store.Management(ctx, created.ID, created.ManagementToken); err != nil || status.Status != "pending_upload" {
				t.Fatalf("pending: %+v %v", status, err)
			}
			tx, err := conn.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.OpenTx(ctx, tx, created.ID, ""); !errors.Is(err, ErrNotFound) {
				_ = tx.Rollback()
				t.Fatal("pending payload released", err)
			}
			_ = tx.Rollback()
			deadline := now.Add(15 * time.Minute)
			if created.ExpiresAt.Before(deadline) {
				deadline = created.ExpiresAt
			}
			now = deadline.Add(-time.Second)
			store.objects = clockAdvancingObjects{objects, func() { now = deadline }}
			if err := store.Finalize(ctx, created.ID); !errors.Is(err, ErrExpired) {
				t.Fatalf("HEAD crossed deadline: %v", err)
			}
			if err := store.Finalize(ctx, created.ID); !errors.Is(err, ErrExpired) {
				t.Fatal("expired pending finalized", err)
			}
			reaper := newTestReaper(t, conn, store, newTestOutbox(t, conn), 1)
			reaper.SetNowForTest(func() time.Time { return now })
			if _, err := reaper.ClaimOnce(ctx); err != nil {
				t.Fatal(err)
			}
			status, err := store.Management(ctx, created.ID, created.ManagementToken)
			if ttl == 3600 {
				if err != nil || status.Status != "unavailable" || status.CanCancel {
					t.Fatalf("orphan outcome lost: %+v %v", status, err)
				}
			} else if !errors.Is(err, ErrManagementUnavailable) {
				t.Fatal("expired management retained", err)
			}
			var count int
			if err := conn.QueryRow(`select count(*) from secrets`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("orphan row retained: %d %v", count, err)
			}
		})
	}
}
