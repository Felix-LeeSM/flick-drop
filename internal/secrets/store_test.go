package secrets

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
)

func TestStoreCreateFileSecret(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	encryptedFilename := *ageField(200)
	contentType := "text/plain"
	created, err := store.Create(ctx, CreateInput{
		Kind:       KindFile,
		Ciphertext: testBody,
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash:   "proof-hash",
		WrappedIdentity:   testWrappedIdentity,
		EncryptedFilename: &encryptedFilename,
		ContentType:       &contentType,
		SizeBytes:         testBodyLen,
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
	if loaded.WrappedIdentity == nil || *loaded.WrappedIdentity != *testWrappedIdentity {
		t.Fatalf("wrapped identity = %v, want the stored age file", loaded.WrappedIdentity)
	}
	if !bytes.Equal(loaded.Ciphertext, testBody) {
		t.Fatalf("ciphertext = %q, want %q", loaded.Ciphertext, testBody)
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
		Ciphertext: testBody,
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash: "proof-hash",
		WrappedIdentity: testWrappedIdentity,
		SizeBytes:       testBodyLen,
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

func TestStoreOpenTxLeavesPayloadForCleanup(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	created, err := store.Create(ctx, CreateInput{
		Kind:       KindText,
		Ciphertext: testBody,
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash: "proof-hash",
		WrappedIdentity: testWrappedIdentity,
		SizeBytes:       testBodyLen,
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
	if _, err := store.OpenTx(ctx, tx, created.ID, "proof-hash"); err != nil {
		t.Fatalf("open tx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	if _, err := store.Get(ctx, created.ID); !errors.Is(err, ErrConsumed) {
		t.Fatalf("get after open error = %v, want ErrConsumed", err)
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
		Ciphertext: testBody,
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash: "proof-hash",
		WrappedIdentity: testWrappedIdentity,
		SizeBytes:       testBodyLen,
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
	if !bytes.Equal(opened.Ciphertext, testBody) {
		t.Fatalf("opened ciphertext = %q, want %q", opened.Ciphertext, testBody)
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
		Ciphertext: testBody,
		AccessKDF: KDFParams{
			Algorithm:     KDFPBKDF2SHA256,
			Salt:          "access-salt",
			Iterations:    600000,
			KeyLengthBits: 256,
		},
		AccessProofHash: "proof-hash",
		WrappedIdentity: testWrappedIdentity,
		SizeBytes:       testBodyLen,
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
		Ciphertext: testBody,
		SizeBytes:  testBodyLen,
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
	if !bytes.Equal(opened.Ciphertext, testBody) {
		t.Fatalf("opened ciphertext = %q, want %q", opened.Ciphertext, testBody)
	}
	if opened.WrappedIdentity != nil {
		t.Fatalf("model B wrapped identity = %q, want none", *opened.WrappedIdentity)
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

func TestStoreRejectsTTLOutsideRange(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, openTestDB(t, ctx))

	for _, ttl := range []int{60, 999999} { // below the 300s floor, above the 604800s ceiling
		_, err := store.Create(ctx, CreateInput{
			Kind:       KindText,
			Ciphertext: testBody,
			SizeBytes:  testBodyLen,
			TTLSeconds: ttl,
			MaxViews:   1,
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("ttl %d: err = %v, want ErrInvalidInput", ttl, err)
		}
	}
}

const testBodyLen = 64

var (
	testBody            = ageBody(testBodyLen)
	testWrappedIdentity = ageField(230)
	testAccessKDF       = KDFParams{Algorithm: KDFPBKDF2SHA256, Salt: "access-salt", Iterations: 600000, KeyLengthBits: 256}
)

// ageBody returns n bytes shaped like an age file: the magic line, then
// padding. The server only checks the magic and the length.
func ageBody(n int) []byte {
	return append([]byte(string(ageMagic)), bytes.Repeat([]byte("x"), n-len(ageMagic))...)
}

// ageField is the base64 of an n-byte age-shaped file, as the API carries
// encrypted filenames and wrapped identities.
func ageField(n int) *string {
	s := base64.StdEncoding.EncodeToString(ageBody(n))
	return &s
}

func ptr(s string) *string { return &s }

func TestStoreCreateValidatesAgeFields(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, openTestDB(t, ctx))

	modelA := func(kind string) CreateInput {
		in := CreateInput{Kind: kind, Ciphertext: testBody, SizeBytes: testBodyLen, TTLSeconds: 600, MaxViews: 1,
			AccessKDF: testAccessKDF, AccessProofHash: "proof-hash", WrappedIdentity: testWrappedIdentity}
		if kind == KindFile {
			in.EncryptedFilename = ageField(200)
		}
		return in
	}
	modelB := func(kind string) CreateInput {
		in := modelA(kind)
		in.AccessKDF, in.AccessProofHash, in.WrappedIdentity = KDFParams{}, "", nil
		return in
	}
	for _, ok := range []CreateInput{modelA(KindText), modelA(KindFile), modelB(KindText), modelB(KindFile)} {
		if _, err := store.Create(ctx, ok); err != nil {
			t.Fatalf("valid %s create (proof %q): %v", ok.Kind, ok.AccessProofHash, err)
		}
	}

	cases := map[string]CreateInput{}
	add := func(name string, base CreateInput, mut func(*CreateInput)) {
		mut(&base)
		cases[name] = base
	}
	add("size one short of the body", modelB(KindText), func(in *CreateInput) { in.SizeBytes = testBodyLen - 1 })
	add("size one past the body", modelB(KindText), func(in *CreateInput) { in.SizeBytes = testBodyLen + 1 })
	add("body without age magic", modelB(KindText), func(in *CreateInput) {
		in.Ciphertext = bytes.Repeat([]byte("x"), testBodyLen)
	})
	add("empty body", modelB(KindText), func(in *CreateInput) { in.Ciphertext, in.SizeBytes = nil, 0 })
	add("model A without wrapped identity", modelA(KindText), func(in *CreateInput) { in.WrappedIdentity = nil })
	add("model B with wrapped identity", modelB(KindText), func(in *CreateInput) { in.WrappedIdentity = testWrappedIdentity })
	add("wrapped identity not an age file", modelA(KindText), func(in *CreateInput) {
		in.WrappedIdentity = ptr(base64.StdEncoding.EncodeToString([]byte("not age")))
	})
	add("wrapped identity not base64", modelA(KindText), func(in *CreateInput) { in.WrappedIdentity = ptr("!!!") })
	add("content type with a newline", modelB(KindFile), func(in *CreateInput) { in.ContentType = ptr("text/plain\r\nX: y") })
	add("content type with non-ASCII", modelB(KindFile), func(in *CreateInput) { in.ContentType = ptr("text/plaïn") })
	add("content type over 255 bytes", modelB(KindFile), func(in *CreateInput) {
		in.ContentType = ptr("application/" + strings.Repeat("x", 244))
	})
	add("wrapped identity over 1 KiB", modelA(KindText), func(in *CreateInput) { in.WrappedIdentity = ageField(maxWrappedIdentityBytes + 1) })
	add("proof without access kdf", modelA(KindText), func(in *CreateInput) { in.AccessKDF = KDFParams{} })
	add("access kdf without proof", modelA(KindText), func(in *CreateInput) { in.AccessProofHash = "" })
	add("weak access kdf", modelA(KindText), func(in *CreateInput) { in.AccessKDF.Iterations = 10 })
	add("file without encrypted filename", modelB(KindFile), func(in *CreateInput) { in.EncryptedFilename = nil })
	add("text with encrypted filename", modelB(KindText), func(in *CreateInput) { in.EncryptedFilename = ageField(200) })
	add("filename not an age file", modelB(KindFile), func(in *CreateInput) {
		in.EncryptedFilename = ptr(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 64))))
	})
	add("filename over 8 KiB", modelB(KindFile), func(in *CreateInput) { in.EncryptedFilename = ageField(maxEncryptedFilenameBytes + 1) })
	for name, in := range cases {
		if _, err := store.Create(ctx, in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
	// The size bounds are inclusive.
	in := modelA(KindFile)
	in.WrappedIdentity, in.EncryptedFilename = ageField(maxWrappedIdentityBytes), ageField(maxEncryptedFilenameBytes)
	if _, err := store.Create(ctx, in); err != nil {
		t.Fatalf("create at wrapped identity and filename caps: %v", err)
	}
}

func TestMaxPlaintextBytesInvertsMaxCiphertextBytes(t *testing.T) {
	floor := MaxCiphertextBytes(0)
	for _, c := range []int64{0, 1, floor - 1} {
		if got := MaxPlaintextBytes(c); got != 0 {
			t.Fatalf("MaxPlaintextBytes(%d) = %d, want 0 below an empty age file", c, got)
		}
	}
	var cs []int64
	for c := floor; c < floor+64; c++ {
		cs = append(cs, c)
	}
	for k := int64(0); k <= 4; k++ {
		edge := MaxCiphertextBytes(k * ageChunkBytes)
		for _, d := range []int64{-17, -16, -1, 0, 1, 16, 17} {
			if edge+d >= floor {
				cs = append(cs, edge+d)
			}
		}
	}
	cs = append(cs, 1<<20, 50<<20, 256<<20)
	for _, c := range cs {
		n := MaxPlaintextBytes(c)
		if MaxCiphertextBytes(n) > c || MaxCiphertextBytes(n+1) <= c {
			t.Fatalf("c=%d: n=%d, MaxCiphertextBytes(n)=%d, MaxCiphertextBytes(n+1)=%d", c, n, MaxCiphertextBytes(n), MaxCiphertextBytes(n+1))
		}
	}
}
