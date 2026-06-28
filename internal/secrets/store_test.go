package secrets

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
)

func TestStoreCreateGetConsume(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	created, err := store.Create(ctx, CreateInput{
		Kind:       KindText,
		Ciphertext: []byte("ciphertext"),
		Nonce:      "nonce",
		KDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash: "proof-hash",
		SizeBytes:       10,
		TTLSeconds:      600,
		MaxViews:        1,
	})
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected generated id")
	}

	loaded, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(loaded.Ciphertext) != "ciphertext" {
		t.Fatalf("ciphertext mismatch: %q", string(loaded.Ciphertext))
	}
	if loaded.KDF.Iterations != 600000 {
		t.Fatalf("kdf iterations mismatch: %d", loaded.KDF.Iterations)
	}

	if err := store.Consume(ctx, created.ID); err != nil {
		t.Fatalf("consume secret: %v", err)
	}
	if _, err := store.Get(ctx, created.ID); !errors.Is(err, ErrConsumed) {
		t.Fatalf("second get error = %v, want ErrConsumed", err)
	}
	if err := store.Consume(ctx, created.ID); !errors.Is(err, ErrConsumed) {
		t.Fatalf("second consume error = %v, want ErrConsumed", err)
	}

	var payloadCount int
	if err := conn.QueryRowContext(ctx, `select count(*) from secret_payloads where secret_id = ?`, created.ID).Scan(&payloadCount); err != nil {
		t.Fatalf("count payloads: %v", err)
	}
	if payloadCount != 0 {
		t.Fatalf("payload count = %d, want 0", payloadCount)
	}
}

func TestStoreCreateFileSecret(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	encryptedFilename := `{"nonce":"filename-nonce","ciphertext":"filename-ciphertext"}`
	contentType := "text/plain"
	created, err := store.Create(ctx, CreateInput{
		Kind:       KindFile,
		Ciphertext: []byte("encrypted-file-bytes"),
		Nonce:      "nonce",
		KDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash:   "proof-hash",
		EncryptedFilename: &encryptedFilename,
		ContentType:       &contentType,
		SizeBytes:         20,
		TTLSeconds:        600,
		MaxViews:          1,
	})
	if err != nil {
		t.Fatalf("create file secret: %v", err)
	}

	loaded, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get file secret: %v", err)
	}
	if loaded.Kind != KindFile {
		t.Fatalf("kind = %q, want %q", loaded.Kind, KindFile)
	}
	if loaded.EncryptedFilename == nil || *loaded.EncryptedFilename != encryptedFilename {
		t.Fatalf("encrypted filename = %v, want %q", loaded.EncryptedFilename, encryptedFilename)
	}
	if loaded.ContentType == nil || *loaded.ContentType != contentType {
		t.Fatalf("content type = %v, want %q", loaded.ContentType, contentType)
	}
	if string(loaded.Ciphertext) != "encrypted-file-bytes" {
		t.Fatalf("ciphertext = %q, want encrypted-file-bytes", string(loaded.Ciphertext))
	}
}

func TestStoreCleanupDeletesPayload(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	created, err := store.Create(ctx, CreateInput{
		Kind:       KindText,
		Ciphertext: []byte("ciphertext"),
		Nonce:      "nonce",
		KDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash: "proof-hash",
		SizeBytes:       10,
		TTLSeconds:      600,
		MaxViews:        1,
	})
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}

	cleaned, err := store.Cleanup(ctx, created.ID)
	if err != nil {
		t.Fatalf("cleanup secret: %v", err)
	}
	if !cleaned {
		t.Fatal("cleanup cleaned = false, want true")
	}

	if _, err := store.Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after cleanup error = %v, want ErrNotFound", err)
	}

	var payloadCount int
	if err := conn.QueryRowContext(ctx, `select count(*) from secret_payloads where secret_id = ?`, created.ID).Scan(&payloadCount); err != nil {
		t.Fatalf("count payloads: %v", err)
	}
	if payloadCount != 0 {
		t.Fatalf("payload count = %d, want 0", payloadCount)
	}

	cleaned, err = store.Cleanup(ctx, created.ID)
	if err != nil {
		t.Fatalf("second cleanup secret: %v", err)
	}
	if cleaned {
		t.Fatal("second cleanup cleaned = true, want false")
	}

	cleaned, err = store.Cleanup(ctx, "missing-secret")
	if err != nil {
		t.Fatalf("missing cleanup secret: %v", err)
	}
	if cleaned {
		t.Fatal("missing cleanup cleaned = true, want false")
	}
}

func TestStoreMarkConsumedTxLeavesPayloadForCleanup(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	created, err := store.Create(ctx, CreateInput{
		Kind:       KindText,
		Ciphertext: []byte("ciphertext"),
		Nonce:      "nonce",
		KDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash: "proof-hash",
		SizeBytes:       10,
		TTLSeconds:      600,
		MaxViews:        1,
	})
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := store.MarkConsumedTx(ctx, tx, created.ID); err != nil {
		t.Fatalf("mark consumed: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	if _, err := store.Get(ctx, created.ID); !errors.Is(err, ErrConsumed) {
		t.Fatalf("get after mark consumed error = %v, want ErrConsumed", err)
	}

	var payloadCount int
	if err := conn.QueryRowContext(ctx, `select count(*) from secret_payloads where secret_id = ?`, created.ID).Scan(&payloadCount); err != nil {
		t.Fatalf("count payloads: %v", err)
	}
	if payloadCount != 1 {
		t.Fatalf("payload count after mark consumed = %d, want 1", payloadCount)
	}

	cleaned, err := store.Cleanup(ctx, created.ID)
	if err != nil {
		t.Fatalf("cleanup consumed payload: %v", err)
	}
	if !cleaned {
		t.Fatal("cleanup cleaned = false, want true")
	}
}

func TestStoreOpenTxRequiresValidProofAndConsumesOnce(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	created, err := store.Create(ctx, CreateInput{
		Kind:       KindText,
		Ciphertext: []byte("ciphertext"),
		Nonce:      "nonce",
		KDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash: "proof-hash",
		SizeBytes:       10,
		TTLSeconds:      600,
		MaxViews:        1,
	})
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin wrong proof tx: %v", err)
	}
	if _, err := store.OpenTx(ctx, tx, created.ID, "wrong-proof-hash"); !errors.Is(err, ErrInvalidAccess) {
		t.Fatalf("wrong proof open error = %v, want ErrInvalidAccess", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback wrong proof tx: %v", err)
	}

	metadata, err := store.Metadata(ctx, created.ID)
	if err != nil {
		t.Fatalf("metadata after wrong proof: %v", err)
	}
	if metadata.AccessKDF.Salt != "access-salt" {
		t.Fatalf("metadata access salt = %q, want access-salt", metadata.AccessKDF.Salt)
	}

	tx, err = conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin correct proof tx: %v", err)
	}
	opened, err := store.OpenTx(ctx, tx, created.ID, "proof-hash")
	if err != nil {
		t.Fatalf("open with proof: %v", err)
	}
	if string(opened.Ciphertext) != "ciphertext" {
		t.Fatalf("opened ciphertext = %q, want ciphertext", string(opened.Ciphertext))
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit open tx: %v", err)
	}

	tx, err = conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin second open tx: %v", err)
	}
	if _, err := store.OpenTx(ctx, tx, created.ID, "proof-hash"); !errors.Is(err, ErrConsumed) {
		t.Fatalf("second open error = %v, want ErrConsumed", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback second open tx: %v", err)
	}
}

func TestStoreOpenTxDeletesPayloadAfterFiveInvalidProofs(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	created, err := store.Create(ctx, CreateInput{
		Kind:       KindText,
		Ciphertext: []byte("ciphertext"),
		Nonce:      "nonce",
		KDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash: "proof-hash",
		SizeBytes:       10,
		TTLSeconds:      600,
		MaxViews:        1,
	})
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}

	for attempt := 1; attempt <= maxFailedAccessAttempts; attempt++ {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin wrong proof tx %d: %v", attempt, err)
		}
		if _, err := store.OpenTx(ctx, tx, created.ID, "wrong-proof-hash"); !errors.Is(err, ErrInvalidAccess) {
			t.Fatalf("wrong proof open attempt %d error = %v, want ErrInvalidAccess", attempt, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit wrong proof tx %d: %v", attempt, err)
		}

		var failedAccessCount int
		if err := conn.QueryRowContext(ctx, `select failed_access_count from secrets where id = ?`, created.ID).Scan(&failedAccessCount); err != nil {
			t.Fatalf("load failed access count after attempt %d: %v", attempt, err)
		}
		if failedAccessCount != attempt {
			t.Fatalf("failed access count after attempt %d = %d, want %d", attempt, failedAccessCount, attempt)
		}
	}

	if _, err := store.Get(ctx, created.ID); !errors.Is(err, ErrConsumed) {
		t.Fatalf("get after failed access limit error = %v, want ErrConsumed", err)
	}

	var payloadCount int
	if err := conn.QueryRowContext(ctx, `select count(*) from secret_payloads where secret_id = ?`, created.ID).Scan(&payloadCount); err != nil {
		t.Fatalf("count payloads after failed access limit: %v", err)
	}
	if payloadCount != 0 {
		t.Fatalf("payload count after failed access limit = %d, want 0", payloadCount)
	}
}

func TestStoreRejectsInvalidKDF(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, openTestDB(t, ctx))

	_, err := store.Create(ctx, CreateInput{
		Kind:       KindText,
		Ciphertext: []byte("ciphertext"),
		Nonce:      "nonce",
		KDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "salt",
			Iterations:    10,
			KeyLengthBits: 256,
		},
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash: "proof-hash",
		SizeBytes:       10,
		TTLSeconds:      600,
		MaxViews:        1,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("create error = %v, want ErrInvalidInput", err)
	}
}

func TestStoreRejectsFileWithoutEncryptedFilename(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, openTestDB(t, ctx))

	_, err := store.Create(ctx, CreateInput{
		Kind:       KindFile,
		Ciphertext: []byte("ciphertext"),
		Nonce:      "nonce",
		KDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash: "proof-hash",
		SizeBytes:       10,
		TTLSeconds:      600,
		MaxViews:        1,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("create error = %v, want ErrInvalidInput", err)
	}
}

func TestCountPendingUploads(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	expires := time.Date(2026, 6, 17, 11, 0, 0, 0, time.UTC)

	insertSecret(t, ctx, conn, secretFixture{id: "pend_1", state: "pending_upload", expiresAt: expires})
	insertSecret(t, ctx, conn, secretFixture{id: "pend_2", state: "pending_upload", expiresAt: expires})
	insertSecret(t, ctx, conn, secretFixture{id: "act_1", state: "active", expiresAt: expires})

	n, err := store.CountPendingUploads(ctx)
	if err != nil {
		t.Fatalf("count pending uploads: %v", err)
	}
	if n != 2 {
		t.Fatalf("pending upload count = %d, want 2", n)
	}
}

func openTestDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()

	conn, err := db.OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	if err := db.MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("migrate api: %v", err)
	}
	return conn
}

func newTestStore(t *testing.T, conn *sql.DB) *Store {
	t.Helper()

	store, err := NewStore(conn, StoreOptions{
		PayloadInlineMaxBytes: 1024,
		MinTTLSeconds:         300,
		MaxTTLSeconds:         604800,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func TestStoreModelBSecretOpensWithoutProof(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	// Model B: no KDF, no access KDF, no access proof hash. The link is the
	// capability; the decryption key travels in the URL fragment.
	created, err := store.Create(ctx, CreateInput{
		Kind:       KindText,
		Ciphertext: []byte("ciphertext"),
		Nonce:      "nonce",
		SizeBytes:  10,
		TTLSeconds: 600,
		MaxViews:   1,
	})
	if err != nil {
		t.Fatalf("create model B secret: %v", err)
	}

	// Model B metadata exposes no access block.
	metadata, err := store.Metadata(ctx, created.ID)
	if err != nil {
		t.Fatalf("metadata model B: %v", err)
	}
	if metadata.AccessKDF.Algorithm != "" {
		t.Fatalf("model B metadata access algorithm = %q, want empty", metadata.AccessKDF.Algorithm)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin model B open tx: %v", err)
	}
	opened, err := store.OpenTx(ctx, tx, created.ID, "")
	if err != nil {
		t.Fatalf("open model B without proof: %v", err)
	}
	if string(opened.Ciphertext) != "ciphertext" {
		t.Fatalf("opened ciphertext = %q, want ciphertext", string(opened.Ciphertext))
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit model B open tx: %v", err)
	}

	// Model B is still one-time: a second open is consumed.
	tx, err = conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin second open tx: %v", err)
	}
	if _, err := store.OpenTx(ctx, tx, created.ID, ""); !errors.Is(err, ErrConsumed) {
		t.Fatalf("second model B open error = %v, want ErrConsumed", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback second open tx: %v", err)
	}
}

func TestStoreRejectsMixedAccessModel(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, openTestDB(t, ctx))

	validKDF := KDFParams{
		Algorithm:     KDFPBKDF2SHA256,
		Salt:          "salt",
		Iterations:    600000,
		KeyLengthBits: 256,
	}

	// Access proof without KDF must be rejected (not silently treated as Model B).
	if _, err := store.Create(ctx, CreateInput{
		Kind:            KindText,
		Ciphertext:      []byte("ciphertext"),
		Nonce:           "nonce",
		AccessProofHash: "proof-hash",
		SizeBytes:       10,
		TTLSeconds:      600,
		MaxViews:        1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("proof without kdf error = %v, want ErrInvalidInput", err)
	}

	// KDF without access proof must be rejected.
	if _, err := store.Create(ctx, CreateInput{
		Kind:       KindText,
		Ciphertext: []byte("ciphertext"),
		Nonce:      "nonce",
		KDF:        validKDF,
		AccessKDF:  validKDF,
		SizeBytes:  10,
		TTLSeconds: 600,
		MaxViews:   1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("kdf without proof error = %v, want ErrInvalidInput", err)
	}
}

func TestStoreRejectsTTLOutsideRange(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, openTestDB(t, ctx))

	for _, ttl := range []int{60, 999999} { // below the 300s floor, above the 604800s ceiling
		_, err := store.Create(ctx, CreateInput{
			Kind:       KindText,
			Ciphertext: []byte("ciphertext"),
			Nonce:      "nonce",
			SizeBytes:  10,
			TTLSeconds: ttl,
			MaxViews:   1,
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("ttl %d: err = %v, want ErrInvalidInput", ttl, err)
		}
	}
}
