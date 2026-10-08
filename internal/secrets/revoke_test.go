package secrets

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

func TestRevokeRemovesAccessAndPreservesOutcome(t *testing.T) {
	for _, state := range []string{"inline", "pending_upload", "active_s3"} {
		for _, proof := range []string{"", "proof"} {
			t.Run(state+"/"+proof, func(t *testing.T) {
				ctx := context.Background()
				conn := openTestDB(t, ctx)
				objects := newMockObjectStore()
				store := newLargeTestStore(t, conn, objects)
				store.outbox = newTestOutbox(t, conn)
				now := time.Date(2026, 10, 8, 1, 0, 0, 123456789, time.UTC)
				store.SetNowForTest(func() time.Time { return now })
				input := CreateInput{Kind: KindText, Nonce: "nonce", Ciphertext: []byte("ciphertext"), SizeBytes: 10, TTLSeconds: 600}
				if proof != "" {
					input.AccessProofHash = proof
					input.KDF = KDFParams{Algorithm: KDFPBKDF2SHA256, Salt: "salt", Iterations: 600000, KeyLengthBits: 256}
					input.AccessKDF = input.KDF
				}
				var id, token string
				if state == "inline" {
					created, err := store.Create(ctx, input)
					if err != nil {
						t.Fatal(err)
					}
					id, token = created.ID, created.ManagementToken
				} else {
					created, err := store.CreateLarge(ctx, CreateLargeInput{Kind: input.Kind, Nonce: input.Nonce, KDF: input.KDF, AccessKDF: input.AccessKDF, AccessProofHash: proof, SizeBytes: 2048, TTLSeconds: 600})
					if err != nil {
						t.Fatal(err)
					}
					id, token = created.ID, created.ManagementToken
					objects.objects[managedObjectPrefix+id] = make([]byte, 2048+AEADOverheadBytes)
					if state == "active_s3" {
						if err := store.Finalize(ctx, id); err != nil {
							t.Fatal(err)
						}
					}
				}
				if _, err := store.Revoke(ctx, id, "bad"); !errors.Is(err, ErrManagementUnavailable) {
					t.Fatalf("wrong token: %v", err)
				}
				for range 2 {
					status, err := store.Revoke(ctx, id, token)
					if err != nil || status.Status != "cancelled" || status.CanCancel {
						t.Fatalf("cancel status %v %v", status, err)
					}
				}
				if countSecrets(t, ctx, conn) != 0 {
					t.Fatal("cancelled delivery retained")
				}
				var payloads int
				if err := conn.QueryRow(`select count(*) from secret_payloads`).Scan(&payloads); err != nil || payloads != 0 {
					t.Fatalf("payloads %d %v", payloads, err)
				}
				tx, err := conn.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				_, err = store.OpenTx(ctx, tx, id, proof)
				_ = tx.Rollback()
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("cancelled open: %v", err)
				}
				if err := store.Finalize(ctx, id); !errors.Is(err, ErrNotFound) {
					t.Fatalf("cancelled finalize: %v", err)
				}
				jobs := readOutboxEvents(t, ctx, conn)
				if state == "inline" {
					if len(jobs) != 0 {
						t.Fatal("inline cancellation needs no object job")
					}
				} else if len(jobs) != 1 || jobs[0].Kind != events.KindDeleteOCIObject || jobs[0].Reason != events.ReasonManual || jobs[0].ObjectKey != managedObjectPrefix+id {
					t.Fatalf("cancel jobs: %v", jobs)
				}
				now = now.Add(10 * time.Minute)
				if _, err := store.Revoke(ctx, id, token); !errors.Is(err, ErrManagementUnavailable) {
					t.Fatalf("expired cancellation: %v", err)
				}
			})
		}
	}
}

func TestRevokeRollsBackOutboxAndCommitFailures(t *testing.T) {
	for _, failure := range []string{"outbox", "commit"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			conn := openTestDB(t, ctx)
			store := newLargeTestStore(t, conn, newMockObjectStore())
			store.outbox = newTestOutbox(t, conn)
			created, err := store.CreateLarge(ctx, CreateLargeInput{Kind: KindText, Nonce: "nonce", SizeBytes: 2048, TTLSeconds: 600})
			if err != nil {
				t.Fatal(err)
			}
			if failure == "outbox" {
				store.outbox = &fakeOutbox{err: errors.New("enqueue failed")}
			} else {
				_, err = conn.Exec(`create table fail_commit (secret_id text references secrets(id) deferrable initially deferred); create trigger fail_cancellation after delete on secrets begin insert into fail_commit values (old.id); end;`)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Revoke(ctx, created.ID, created.ManagementToken); err == nil {
				t.Fatal("failure reported cancellation success")
			}
			status, err := store.Management(ctx, created.ID, created.ManagementToken)
			if err != nil || status.Status != "pending_upload" {
				t.Fatalf("rollback status %v %v", status, err)
			}
			if len(readOutboxEvents(t, ctx, conn)) != 0 {
				t.Fatal("rollback retained a job")
			}
		})
	}
}

type blockingObjects struct {
	storage.ObjectStore
	entered  chan struct{}
	release  chan struct{}
	blockGet bool
}

func (o blockingObjects) Get(ctx context.Context, key string) ([]byte, error) {
	if o.blockGet {
		close(o.entered)
		select {
		case <-o.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return o.ObjectStore.Get(ctx, key)
}
func (o blockingObjects) Head(ctx context.Context, key string) (storage.ObjectInfo, error) {
	if !o.blockGet {
		close(o.entered)
		select {
		case <-o.release:
		case <-ctx.Done():
			return storage.ObjectInfo{}, ctx.Err()
		}
	}
	return o.ObjectStore.Head(ctx, key)
}

func TestRevokeWinsAgainstInFlightObjectRead(t *testing.T) {
	for _, operation := range []string{"open", "finalize"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), "api.db")
			first, err := db.OpenSQLite(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			if err := db.MigrateAPI(ctx, first); err != nil {
				t.Fatal(err)
			}
			second, err := db.OpenSQLite(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			objects := newMockObjectStore()
			store := newLargeTestStore(t, first, objects)
			revoker := newLargeTestStore(t, second, objects)
			revoker.outbox = newTestOutbox(t, second)
			created, err := store.CreateLarge(ctx, CreateLargeInput{Kind: KindText, Nonce: "nonce", SizeBytes: 2048, TTLSeconds: 600})
			if err != nil {
				t.Fatal(err)
			}
			objects.objects[managedObjectPrefix+created.ID] = make([]byte, 2048+AEADOverheadBytes)
			if operation == "open" {
				if err := store.Finalize(ctx, created.ID); err != nil {
					t.Fatal(err)
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			store.objects = blockingObjects{objects, entered, release, operation == "open"}
			done := make(chan error, 1)
			go func() {
				if operation == "finalize" {
					done <- store.Finalize(ctx, created.ID)
					return
				}
				tx, err := first.BeginTx(ctx, nil)
				if err != nil {
					done <- err
					return
				}
				defer tx.Rollback()
				_, err = store.OpenTx(ctx, tx, created.ID, "")
				if err == nil {
					err = tx.Commit()
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			status, err := revoker.Revoke(ctx, created.ID, created.ManagementToken)
			close(release)
			if err != nil || status.Status != "cancelled" {
				t.Fatalf("cancel: %v %v", status, err)
			}
			if err := <-done; err == nil {
				t.Fatal("in-flight operation succeeded after committed cancellation")
			}
			status, err = revoker.Management(ctx, created.ID, created.ManagementToken)
			if err != nil || status.Status != "cancelled" {
				t.Fatalf("cancel outcome changed: %v %v", status, err)
			}
		})
	}
}

func TestRevokeRacesOpenAndProofLockout(t *testing.T) {
	for _, operation := range []string{"open", "lockout"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			conn := openTestDB(t, ctx)
			store := newTestStore(t, conn)
			kdf := KDFParams{Algorithm: KDFPBKDF2SHA256, Salt: "salt", Iterations: 600000, KeyLengthBits: 256}
			created, err := store.Create(ctx, CreateInput{Kind: KindText, Nonce: "nonce", Ciphertext: []byte("ciphertext"), SizeBytes: 10, TTLSeconds: 600, KDF: kdf, AccessKDF: kdf, AccessProofHash: "proof"})
			if err != nil {
				t.Fatal(err)
			}
			open := func(proof string) error {
				tx, err := conn.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				_, err = store.OpenTx(ctx, tx, created.ID, proof)
				if err == nil || errors.Is(err, ErrInvalidAccess) {
					if commitErr := tx.Commit(); commitErr != nil {
						return commitErr
					}
				}
				return err
			}
			proof := "proof"
			if operation == "lockout" {
				proof = "wrong"
				for range 4 {
					if err := open(proof); !errors.Is(err, ErrInvalidAccess) {
						t.Fatal(err)
					}
				}
			}
			start := make(chan struct{})
			opened, cancelled := make(chan error, 1), make(chan error, 1)
			go func() { <-start; opened <- open(proof) }()
			go func() { <-start; _, err := store.Revoke(ctx, created.ID, created.ManagementToken); cancelled <- err }()
			close(start)
			openErr, cancelErr := <-opened, <-cancelled
			status, err := store.Management(ctx, created.ID, created.ManagementToken)
			if err != nil {
				t.Fatal(err)
			}
			if cancelErr == nil {
				if status.Status != "cancelled" || !errors.Is(openErr, ErrNotFound) {
					t.Fatalf("cancel winner: %v %v", status, openErr)
				}
			} else {
				want := "opened"
				if operation == "lockout" {
					want = "locked"
				}
				if !errors.Is(cancelErr, ErrNotCancellable) || status.Status != want {
					t.Fatalf("recipient winner: %v %v", status, cancelErr)
				}
				if (operation == "open" && openErr != nil) || (operation == "lockout" && !errors.Is(openErr, ErrInvalidAccess)) {
					t.Fatal(openErr)
				}
			}
		})
	}
}

func TestRevokeHonorsPendingAndContentDeadlines(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newLargeTestStore(t, conn, newMockObjectStore())
	store.outbox = newTestOutbox(t, conn)
	now := time.Date(2026, 10, 8, 1, 0, 0, 123456789, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	created, err := store.CreateLarge(ctx, CreateLargeInput{Kind: KindText, Nonce: "nonce", SizeBytes: 2048, TTLSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(15 * time.Minute)
	for range 2 {
		status, err := store.Revoke(ctx, created.ID, created.ManagementToken)
		if !errors.Is(err, ErrNotCancellable) || status.Status != "unavailable" {
			t.Fatalf("pending deadline cancellation: %v %v", status, err)
		}
		reaper := newTestReaper(t, conn, store, newTestOutbox(t, conn), 1)
		reaper.SetNowForTest(func() time.Time { return now })
		if _, err := reaper.ClaimOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	now = created.ExpiresAt
	if _, err := store.Revoke(ctx, created.ID, created.ManagementToken); !errors.Is(err, ErrManagementUnavailable) {
		t.Fatal(err)
	}
}
