package secrets

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Felix-LeeSM/burn-links/internal/db"
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
		SizeBytes:  10,
		TTLSeconds: 600,
		MaxViews:   1,
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
		SizeBytes:  10,
		TTLSeconds: 600,
		MaxViews:   1,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("create error = %v, want ErrInvalidInput", err)
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
		AllowedTTLSeconds:     []int{600, 3600, 86400},
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}
