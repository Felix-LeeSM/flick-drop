package secrets

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

const (
	KindText                = "text"
	KindFile                = "file"
	StorageSQLite           = "sqlite_blob"
	StorageS3               = "s3_object"
	KDFPBKDF2SHA256         = "PBKDF2-SHA-256"
	maxFailedAccessAttempts = 5
)

type KDFParams struct {
	Algorithm     string `json:"algorithm"`
	Salt          string `json:"salt"`
	Iterations    int    `json:"iterations"`
	KeyLengthBits int    `json:"key_length_bits"`
}

type CreateInput struct {
	Kind              string
	Ciphertext        []byte
	Nonce             string
	KDF               KDFParams
	AccessKDF         KDFParams
	AccessProofHash   string
	EncryptedFilename *string
	ContentType       *string
	SizeBytes         int64
	TTLSeconds        int
	MaxViews          int
}

// CreateLargeInput carries the encryption metadata for a large payload that the
// server never sees: the client uploads the ciphertext directly to the bucket
// via a presigned POST, then /finalize activates the secret. The plaintext
// SizeBytes is informational; the ciphertext cap is the object store's
// maxObjectBytes, enforced by the POST policy's content-length-range.
type CreateLargeInput struct {
	Kind              string
	Nonce             string
	KDF               KDFParams
	AccessKDF         KDFParams
	AccessProofHash   string
	EncryptedFilename *string
	ContentType       *string
	SizeBytes         int64
	TTLSeconds        int
	MaxViews          int
}

type CreateLargeResult struct {
	ID        string
	ExpiresAt time.Time
	Upload    storage.POSTForm
}

type Secret struct {
	ID                string
	Kind              string
	StorageBackend    string
	StorageKey        string
	Ciphertext        []byte
	Nonce             string
	KDF               KDFParams
	AccessKDF         KDFParams
	EncryptedFilename *string
	ContentType       *string
	SizeBytes         int64
	ExpiresAt         time.Time
	accessProofHash   string
}

type Metadata struct {
	ID        string
	Kind      string
	AccessKDF KDFParams
	SizeBytes int64
	ExpiresAt time.Time
}

type Store struct {
	db                    *sql.DB
	now                   func() time.Time
	payloadInlineMaxBytes int64
	maxObjectBytes        int64
	presignTTL            time.Duration
	pendingTTL            time.Duration
	minTTL                int
	maxTTL                int
	objects               storage.ObjectStore
}

type StoreOptions struct {
	PayloadInlineMaxBytes int64
	MaxObjectBytes        int64
	PresignTTL            time.Duration
	PendingTTL            time.Duration
	MinTTLSeconds         int
	MaxTTLSeconds         int
	Objects               storage.ObjectStore
}

func NewStore(db *sql.DB, opts StoreOptions) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("db is required")
	}
	if opts.PayloadInlineMaxBytes <= 0 {
		return nil, fmt.Errorf("payload inline max bytes must be positive")
	}
	if opts.MinTTLSeconds <= 0 {
		return nil, fmt.Errorf("min ttl seconds must be positive")
	}
	if opts.MaxTTLSeconds < opts.MinTTLSeconds {
		return nil, fmt.Errorf("max ttl seconds must be >= min ttl seconds")
	}
	if opts.Objects != nil && opts.MaxObjectBytes <= 0 {
		return nil, fmt.Errorf("max object bytes must be positive when object storage is enabled")
	}

	presignTTL := opts.PresignTTL
	if presignTTL <= 0 {
		presignTTL = 5 * time.Minute
	}
	pendingTTL := opts.PendingTTL
	if pendingTTL <= 0 {
		pendingTTL = 15 * time.Minute
	}

	return &Store{
		db:                    db,
		now:                   func() time.Time { return time.Now().UTC() },
		payloadInlineMaxBytes: opts.PayloadInlineMaxBytes,
		maxObjectBytes:        opts.MaxObjectBytes,
		presignTTL:            presignTTL,
		pendingTTL:            pendingTTL,
		minTTL:                opts.MinTTLSeconds,
		maxTTL:                opts.MaxTTLSeconds,
		objects:               opts.Objects,
	}, nil
}

func (s *Store) SetNowForTest(now func() time.Time) {
	s.now = now
}

func (s *Store) Create(ctx context.Context, input CreateInput) (Secret, error) {
	if input.MaxViews == 0 {
		input.MaxViews = 1
	}
	if err := s.validateCreate(input); err != nil {
		return Secret{}, err
	}

	id, err := generateID()
	if err != nil {
		return Secret{}, err
	}

	now := s.now().UTC()
	expiresAt := now.Add(time.Duration(input.TTLSeconds) * time.Second)

	// Model A secrets store encryption + access KDF and an access proof hash.
	// Model B secrets (passphrase optional) carry none of these: the decryption
	// key travels in the URL fragment, so the columns stay NULL.
	var (
		kdfAlgorithm, kdfSalt, kdfParamsJSON any
		accessKDFParamsJSON, accessProofHash any
	)
	if input.AccessProofHash != "" {
		kdfJSON, err := json.Marshal(input.KDF)
		if err != nil {
			return Secret{}, fmt.Errorf("marshal kdf params: %w", err)
		}
		accessKDFJSON, err := json.Marshal(input.AccessKDF)
		if err != nil {
			return Secret{}, fmt.Errorf("marshal access kdf params: %w", err)
		}
		kdfAlgorithm = input.KDF.Algorithm
		kdfSalt = input.KDF.Salt
		kdfParamsJSON = string(kdfJSON)
		accessKDFParamsJSON = string(accessKDFJSON)
		accessProofHash = input.AccessProofHash
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Secret{}, fmt.Errorf("begin create secret: %w", err)
	}
	defer rollback(tx)

	_, err = tx.ExecContext(ctx, `insert into secrets (
		id, kind, storage_backend, storage_key, nonce, kdf_algorithm, kdf_salt,
		kdf_params_json, access_kdf_params_json, access_proof_hash,
		encrypted_filename, content_type, size_bytes, max_views,
		view_count, expires_at, created_at, updated_at
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
		id,
		input.Kind,
		StorageSQLite,
		id,
		input.Nonce,
		kdfAlgorithm,
		kdfSalt,
		kdfParamsJSON,
		accessKDFParamsJSON,
		accessProofHash,
		input.EncryptedFilename,
		input.ContentType,
		input.SizeBytes,
		input.MaxViews,
		formatTime(expiresAt),
		formatTime(now),
		formatTime(now),
	)
	if err != nil {
		return Secret{}, fmt.Errorf("insert secret metadata: %w", err)
	}

	_, err = tx.ExecContext(ctx, `insert into secret_payloads (
		secret_id, ciphertext, created_at
	) values (?, ?, ?)`, id, input.Ciphertext, formatTime(now))
	if err != nil {
		return Secret{}, fmt.Errorf("insert secret payload: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Secret{}, fmt.Errorf("commit create secret: %w", err)
	}

	return Secret{
		ID:                id,
		Kind:              input.Kind,
		StorageBackend:    StorageSQLite,
		StorageKey:        id,
		Ciphertext:        append([]byte(nil), input.Ciphertext...),
		Nonce:             input.Nonce,
		KDF:               input.KDF,
		AccessKDF:         input.AccessKDF,
		EncryptedFilename: input.EncryptedFilename,
		ContentType:       input.ContentType,
		SizeBytes:         input.SizeBytes,
		ExpiresAt:         expiresAt,
	}, nil
}

// CreateLarge stages a pending_upload secret backed by S3 and returns a
// presigned POST so the client uploads the ciphertext straight to the bucket.
// The server never sees the ciphertext. /finalize flips the row to active once
// the object is confirmed.
func (s *Store) CreateLarge(ctx context.Context, input CreateLargeInput) (CreateLargeResult, error) {
	if s.objects == nil {
		return CreateLargeResult{}, ErrPayloadTooLarge
	}
	if input.MaxViews == 0 {
		input.MaxViews = 1
	}
	if err := s.validateCreateLarge(input); err != nil {
		return CreateLargeResult{}, err
	}

	id, err := generateID()
	if err != nil {
		return CreateLargeResult{}, err
	}

	now := s.now().UTC()
	expiresAt := now.Add(time.Duration(input.TTLSeconds) * time.Second)

	var (
		kdfAlgorithm, kdfSalt, kdfParamsJSON any
		accessKDFParamsJSON, accessProofHash any
	)
	if input.AccessProofHash != "" {
		kdfJSON, err := json.Marshal(input.KDF)
		if err != nil {
			return CreateLargeResult{}, fmt.Errorf("marshal kdf params: %w", err)
		}
		accessKDFJSON, err := json.Marshal(input.AccessKDF)
		if err != nil {
			return CreateLargeResult{}, fmt.Errorf("marshal access kdf params: %w", err)
		}
		kdfAlgorithm = input.KDF.Algorithm
		kdfSalt = input.KDF.Salt
		kdfParamsJSON = string(kdfJSON)
		accessKDFParamsJSON = string(accessKDFJSON)
		accessProofHash = input.AccessProofHash
	}

	// Presign first (pure signing, no DB). A failure returns before any row is
	// inserted, so no orphan pending_upload row is left for a reaper to clean.
	upload, err := s.objects.PresignPOST(ctx, id, s.maxObjectBytes, s.presignTTL)
	if err != nil {
		return CreateLargeResult{}, fmt.Errorf("presign upload: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreateLargeResult{}, fmt.Errorf("begin create large secret: %w", err)
	}
	defer rollback(tx)

	_, err = tx.ExecContext(ctx, `insert into secrets (
		id, kind, storage_backend, storage_key, nonce, kdf_algorithm, kdf_salt,
		kdf_params_json, access_kdf_params_json, access_proof_hash,
		encrypted_filename, content_type, size_bytes, max_views,
		view_count, state, expires_at, created_at, updated_at
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'pending_upload', ?, ?, ?)`,
		id,
		input.Kind,
		StorageS3,
		id,
		input.Nonce,
		kdfAlgorithm,
		kdfSalt,
		kdfParamsJSON,
		accessKDFParamsJSON,
		accessProofHash,
		input.EncryptedFilename,
		input.ContentType,
		input.SizeBytes,
		input.MaxViews,
		formatTime(expiresAt),
		formatTime(now),
		formatTime(now),
	)
	if err != nil {
		return CreateLargeResult{}, fmt.Errorf("insert large secret metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CreateLargeResult{}, fmt.Errorf("commit create large secret: %w", err)
	}

	return CreateLargeResult{ID: id, ExpiresAt: expiresAt, Upload: upload}, nil
}

// Finalize confirms a pending_upload secret: it HEADs the object to verify it
// exists and is within the ciphertext cap, then flips state to active. The
// size check uses the object's real byte length (the ciphertext), not the
// plaintext size_bytes, since AES-GCM adds a tag. Idempotent — finalizing an
// already-active secret is a no-op success.
func (s *Store) Finalize(ctx context.Context, id string) error {
	if s.objects == nil {
		return ErrPayloadTooLarge
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finalize: %w", err)
	}
	defer rollback(tx)

	var state, storageKey, expiresRaw string
	err = tx.QueryRowContext(ctx, `select state, storage_key, expires_at from secrets where id = ?`, id).
		Scan(&state, &storageKey, &expiresRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load pending secret: %w", err)
	}
	if state == "active" {
		return nil
	}
	if state != "pending_upload" {
		return ErrNotPending
	}
	expiresAt, err := parseTime(expiresRaw)
	if err != nil {
		return fmt.Errorf("parse expires_at: %w", err)
	}
	if !s.now().UTC().Before(expiresAt) {
		return ErrExpired
	}

	info, err := s.objects.Head(ctx, storageKey)
	if err != nil {
		return fmt.Errorf("head uploaded object: %w", err)
	}
	if !info.Exists || info.Size > s.maxObjectBytes {
		return ErrObjectMissing
	}

	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `update secrets set state='active', updated_at=? where id=? and state='pending_upload'`, formatTime(now), id); err != nil {
		return fmt.Errorf("activate secret: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit finalize: %w", err)
	}
	return nil
}

// PendingTTL is how long a pending_upload secret may wait for /finalize before
// an orphan sweep reclaims it. Exposed for the sweep caller.
func (s *Store) PendingTTL() time.Duration { return s.pendingTTL }

func (s *Store) Get(ctx context.Context, id string) (Secret, error) {
	secret, consumedAt, err := s.load(ctx, s.db, id)
	if err != nil {
		return Secret{}, err
	}
	if consumedAt.Valid {
		return Secret{}, ErrConsumed
	}
	if !s.now().UTC().Before(secret.ExpiresAt) {
		return Secret{}, ErrExpired
	}
	if err := s.fillCiphertext(ctx, &secret); err != nil {
		return Secret{}, err
	}
	return secret, nil
}

func (s *Store) Metadata(ctx context.Context, id string) (Metadata, error) {
	if id == "" {
		return Metadata{}, ErrNotFound
	}

	var metadata Metadata
	var accessKDFJSON sql.NullString
	var expiresRaw string
	var consumedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `select
			s.id, s.kind, s.access_kdf_params_json, s.size_bytes, s.expires_at, s.consumed_at
		from secrets s
		where s.id = ? and (s.storage_backend = 's3_object'
			or exists (select 1 from secret_payloads p where p.secret_id = s.id))`,
		id,
	).Scan(
		&metadata.ID,
		&metadata.Kind,
		&accessKDFJSON,
		&metadata.SizeBytes,
		&expiresRaw,
		&consumedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Metadata{}, ErrNotFound
	}
	if err != nil {
		return Metadata{}, fmt.Errorf("load secret metadata: %w", err)
	}
	if consumedAt.Valid {
		return Metadata{}, ErrConsumed
	}
	// Model A secrets expose access KDF so the browser can derive the proof.
	// Model B secrets carry no access proof, so the column is NULL and
	// metadata.AccessKDF stays zero — the caller treats an absent access block
	// as passphrase-optional.
	if accessKDFJSON.Valid {
		if err := json.Unmarshal([]byte(accessKDFJSON.String), &metadata.AccessKDF); err != nil {
			return Metadata{}, fmt.Errorf("decode access kdf params: %w", err)
		}
	}
	expiresAt, err := parseTime(expiresRaw)
	if err != nil {
		return Metadata{}, fmt.Errorf("parse expires_at: %w", err)
	}
	metadata.ExpiresAt = expiresAt
	if !s.now().UTC().Before(metadata.ExpiresAt) {
		return Metadata{}, ErrExpired
	}
	return metadata, nil
}

// Consume marks a secret consumed and deletes its inline payload. It does not
// touch S3 objects; production callers use OpenTx (via /open), which enqueues
// delete_oci_object for s3_object secrets. Retained for tests; no production
// caller.
func (s *Store) Consume(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin consume secret: %w", err)
	}
	defer rollback(tx)

	if err := s.MarkConsumedTx(ctx, tx, id); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `delete from secret_payloads where secret_id = ?`, id); err != nil {
		return fmt.Errorf("delete consumed payload: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit consume secret: %w", err)
	}
	return nil
}

func (s *Store) MarkConsumedTx(ctx context.Context, tx *sql.Tx, id string) error {
	if tx == nil {
		return fmt.Errorf("transaction is required")
	}

	now := s.now().UTC()
	secret, consumedAt, err := s.load(ctx, tx, id)
	if err != nil {
		return err
	}
	if consumedAt.Valid {
		return ErrConsumed
	}
	if !now.Before(secret.ExpiresAt) {
		return ErrExpired
	}

	result, err := tx.ExecContext(ctx, `update secrets
		set view_count = view_count + 1,
			consumed_at = ?,
			updated_at = ?
		where id = ? and consumed_at is null`,
		formatTime(now),
		formatTime(now),
		id,
	)
	if err != nil {
		return fmt.Errorf("mark secret consumed: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read consume row count: %w", err)
	}
	if affected != 1 {
		return ErrConsumed
	}
	return nil
}

func (s *Store) OpenTx(ctx context.Context, tx *sql.Tx, id string, accessProofHash string) (Secret, error) {
	if tx == nil {
		return Secret{}, fmt.Errorf("transaction is required")
	}

	now := s.now().UTC()
	secret, consumedAt, err := s.load(ctx, tx, id)
	if err != nil {
		return Secret{}, err
	}
	if consumedAt.Valid {
		return Secret{}, ErrConsumed
	}
	if !now.Before(secret.ExpiresAt) {
		return Secret{}, ErrExpired
	}

	// Model A secrets carry an access proof hash and must be authorized by a
	// matching proof. Model B secrets (passphrase optional) carry none: the link
	// is the capability, so no proof is verified. Because max_views stays 1, a
	// captured fragment key can authorize at most a single open.
	if secret.accessProofHash != "" {
		if subtle.ConstantTimeCompare([]byte(secret.accessProofHash), []byte(accessProofHash)) != 1 {
			if err := s.recordFailedAccessTx(ctx, tx, id, secret.StorageBackend, secret.StorageKey, now); err != nil {
				return Secret{}, err
			}
			return Secret{}, ErrInvalidAccess
		}
	}

	// Fetch the ciphertext from the object store (for s3_object secrets) before
	// marking consumed, so a transient bucket failure leaves the secret
	// reopenable rather than consumed-but-unreadable.
	if err := s.fillCiphertext(ctx, &secret); err != nil {
		return Secret{}, err
	}

	result, err := tx.ExecContext(ctx, `update secrets
		set view_count = view_count + 1,
			consumed_at = ?,
			updated_at = ?
		where id = ? and consumed_at is null`,
		formatTime(now),
		formatTime(now),
		id,
	)
	if err != nil {
		return Secret{}, fmt.Errorf("mark secret opened: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Secret{}, fmt.Errorf("read open row count: %w", err)
	}
	if affected != 1 {
		return Secret{}, ErrConsumed
	}
	return secret, nil
}

// fillCiphertext loads the payload for an S3-backed secret from the object
// store. SQLite-backed secrets already carry their ciphertext in load.
func (s *Store) fillCiphertext(ctx context.Context, secret *Secret) error {
	if secret.StorageBackend != StorageS3 {
		return nil
	}
	if s.objects == nil {
		return ErrNotFound
	}
	ciphertext, err := s.objects.Get(ctx, secret.StorageKey)
	if err != nil {
		return fmt.Errorf("load object payload: %w", err)
	}
	secret.Ciphertext = ciphertext
	return nil
}

func (s *Store) recordFailedAccessTx(ctx context.Context, tx *sql.Tx, id string, storageBackend, storageKey string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `update secrets
		set failed_access_count = failed_access_count + 1,
			consumed_at = case
				when failed_access_count + 1 >= ? then ?
				else consumed_at
			end,
			updated_at = ?
		where id = ? and consumed_at is null`,
		maxFailedAccessAttempts,
		formatTime(now),
		formatTime(now),
		id,
	)
	if err != nil {
		return fmt.Errorf("record failed secret access: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read failed access row count: %w", err)
	}
	if affected != 1 {
		return ErrConsumed
	}

	var failedAccessCount int
	var consumedAt sql.NullString
	if err := tx.QueryRowContext(ctx, `select failed_access_count, consumed_at from secrets where id = ?`, id).Scan(&failedAccessCount, &consumedAt); err != nil {
		return fmt.Errorf("load failed secret access count: %w", err)
	}
	if failedAccessCount >= maxFailedAccessAttempts && consumedAt.Valid {
		// Lock reached: purge the payload so it can never be read. S3-backed
		// secrets have no inline row, so delete the object instead — best-effort:
		// a failed delete must not roll back the lockout count (that would gift
		// the attacker another attempt). An orphaned object is reclaimed by the
		// expiration reaper (issue #76).
		if storageBackend == StorageS3 && s.objects != nil {
			_ = s.objects.Delete(ctx, storageKey)
		} else if _, err := tx.ExecContext(ctx, `delete from secret_payloads where secret_id = ?`, id); err != nil {
			return fmt.Errorf("delete locked secret payload: %w", err)
		}
	}
	return nil
}

func (s *Store) Cleanup(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, ErrNotFound
	}

	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin cleanup secret: %w", err)
	}
	defer rollback(tx)

	result, err := tx.ExecContext(ctx, `delete from secret_payloads where secret_id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("delete secret payload: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read cleanup row count: %w", err)
	}
	if affected > 0 {
		if _, err := tx.ExecContext(ctx, `update secrets set updated_at = ? where id = ?`, formatTime(now), id); err != nil {
			return false, fmt.Errorf("mark secret cleaned: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit cleanup secret: %w", err)
	}
	return affected > 0, nil
}

// ReclaimTx hard-deletes a secret and its inline payload within the caller's
// transaction. It is the reaper's row-cleanup path: orphan (pending_upload) and
// expired secrets are removed here outright, unlike the payload-only Cleanup
// path which preserves consumed metadata. Guarded by reclaim_enqueued_at IS
// NOT NULL so only rows the reaper has claimed can be reaped. Idempotent: a
// missing row is not an error.
func (s *Store) ReclaimTx(ctx context.Context, tx *sql.Tx, id string) error {
	if id == "" {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `delete from secret_payloads where secret_id = ?`, id); err != nil {
		return fmt.Errorf("delete secret payload on reclaim: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from secrets where id = ? and reclaim_enqueued_at is not null`, id); err != nil {
		return fmt.Errorf("delete secret on reclaim: %w", err)
	}
	return nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) load(ctx context.Context, q queryer, id string) (Secret, sql.NullString, error) {
	if id == "" {
		return Secret{}, sql.NullString{}, ErrNotFound
	}

	var secret Secret
	var ciphertext []byte
	var kdfJSON sql.NullString
	var accessKDFJSON sql.NullString
	var accessProofHash sql.NullString
	var encryptedFilename sql.NullString
	var contentType sql.NullString
	var storageBackend sql.NullString
	var storageKey sql.NullString
	var expiresRaw string
	var consumedAt sql.NullString
	var failedAccessCount int

	err := q.QueryRowContext(ctx, `select
			s.id, s.kind, s.storage_backend, s.storage_key, p.ciphertext, s.nonce,
			s.kdf_params_json, s.access_kdf_params_json, s.access_proof_hash,
			s.encrypted_filename, s.content_type, s.size_bytes, s.expires_at,
			s.consumed_at, s.failed_access_count
		from secrets s
		left join secret_payloads p on p.secret_id = s.id
		where s.id = ?`,
		id,
	).Scan(
		&secret.ID,
		&secret.Kind,
		&storageBackend,
		&storageKey,
		&ciphertext,
		&secret.Nonce,
		&kdfJSON,
		&accessKDFJSON,
		&accessProofHash,
		&encryptedFilename,
		&contentType,
		&secret.SizeBytes,
		&expiresRaw,
		&consumedAt,
		&failedAccessCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, sql.NullString{}, ErrNotFound
	}
	if err != nil {
		return Secret{}, sql.NullString{}, fmt.Errorf("load secret: %w", err)
	}
	secret.StorageBackend = storageBackend.String
	secret.StorageKey = storageKey.String
	// SQLite-backed secrets carry ciphertext inline; S3-backed secrets fetch it
	// on demand from the object store, so an empty blob here is expected for
	// pending/active s3_object rows.
	if secret.StorageBackend != StorageS3 && len(ciphertext) == 0 && !consumedAt.Valid {
		return Secret{}, sql.NullString{}, ErrNotFound
	}
	secret.Ciphertext = ciphertext

	// kdf_params_json is present for Model A (passphrase-derived key) and NULL
	// for Model B (random fragment key). Access KDF and proof hash must be both
	// present (Model A) or both absent (Model B); anything else is corruption.
	if kdfJSON.Valid {
		if err := json.Unmarshal([]byte(kdfJSON.String), &secret.KDF); err != nil {
			return Secret{}, sql.NullString{}, fmt.Errorf("decode kdf params: %w", err)
		}
	}
	if accessKDFJSON.Valid != accessProofHash.Valid {
		return Secret{}, sql.NullString{}, ErrInvalidInput
	}
	if accessKDFJSON.Valid {
		if err := json.Unmarshal([]byte(accessKDFJSON.String), &secret.AccessKDF); err != nil {
			return Secret{}, sql.NullString{}, fmt.Errorf("decode access kdf params: %w", err)
		}
		secret.accessProofHash = accessProofHash.String
	}
	expiresAt, err := parseTime(expiresRaw)
	if err != nil {
		return Secret{}, sql.NullString{}, fmt.Errorf("parse expires_at: %w", err)
	}
	secret.ExpiresAt = expiresAt

	if encryptedFilename.Valid {
		secret.EncryptedFilename = &encryptedFilename.String
	}
	if contentType.Valid {
		secret.ContentType = &contentType.String
	}

	return secret, consumedAt, nil
}

func (s *Store) validateCreate(input CreateInput) error {
	if input.Kind != KindText && input.Kind != KindFile {
		return ErrUnsupportedKind
	}
	if len(input.Ciphertext) == 0 || input.Nonce == "" {
		return ErrInvalidInput
	}
	if int64(len(input.Ciphertext)) > s.payloadInlineMaxBytes {
		return ErrPayloadTooLarge
	}
	if input.SizeBytes < 0 {
		return ErrInvalidInput
	}
	if input.TTLSeconds < s.minTTL || input.TTLSeconds > s.maxTTL {
		return ErrInvalidInput
	}
	if input.MaxViews != 1 {
		return ErrUnsupportedViews
	}

	// A secret is either Model A or Model B, never a mix.
	// Model A (passphrase): encryption KDF + access KDF + access proof hash all present.
	// Model B (passphrase optional): all three absent — the random key travels in
	// the URL fragment and the server stores no KDF or proof.
	hasProof := input.AccessProofHash != ""
	hasKDF := input.KDF.Algorithm != ""
	hasAccessKDF := input.AccessKDF.Algorithm != ""
	if hasProof != hasKDF || hasProof != hasAccessKDF {
		return ErrInvalidInput
	}
	if hasProof {
		if input.KDF.Algorithm != KDFPBKDF2SHA256 || input.KDF.Salt == "" || input.KDF.Iterations < 600000 || input.KDF.KeyLengthBits != 256 {
			return ErrInvalidInput
		}
		if input.AccessKDF.Algorithm != KDFPBKDF2SHA256 || input.AccessKDF.Salt == "" || input.AccessKDF.Iterations < 600000 || input.AccessKDF.KeyLengthBits != 256 {
			return ErrInvalidInput
		}
	}
	if input.Kind == KindFile {
		if input.EncryptedFilename == nil || *input.EncryptedFilename == "" {
			return ErrInvalidInput
		}
	}
	return nil
}

// validateCreateLarge mirrors validateCreate minus the ciphertext checks: the
// payload never touches the server for the large path.
func (s *Store) validateCreateLarge(input CreateLargeInput) error {
	if input.Kind != KindText && input.Kind != KindFile {
		return ErrUnsupportedKind
	}
	if input.Nonce == "" {
		return ErrInvalidInput
	}
	if input.SizeBytes < 0 {
		return ErrInvalidInput
	}
	if input.TTLSeconds < s.minTTL || input.TTLSeconds > s.maxTTL {
		return ErrInvalidInput
	}
	if input.MaxViews != 1 {
		return ErrUnsupportedViews
	}

	hasProof := input.AccessProofHash != ""
	hasKDF := input.KDF.Algorithm != ""
	hasAccessKDF := input.AccessKDF.Algorithm != ""
	if hasProof != hasKDF || hasProof != hasAccessKDF {
		return ErrInvalidInput
	}
	if hasProof {
		if input.KDF.Algorithm != KDFPBKDF2SHA256 || input.KDF.Salt == "" || input.KDF.Iterations < 600000 || input.KDF.KeyLengthBits != 256 {
			return ErrInvalidInput
		}
		if input.AccessKDF.Algorithm != KDFPBKDF2SHA256 || input.AccessKDF.Salt == "" || input.AccessKDF.Iterations < 600000 || input.AccessKDF.KeyLengthBits != 256 {
			return ErrInvalidInput
		}
	}
	if input.Kind == KindFile {
		if input.EncryptedFilename == nil || *input.EncryptedFilename == "" {
			return ErrInvalidInput
		}
	}
	return nil
}

func generateID() (string, error) {
	var bytes [18]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate secret id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(raw string) (time.Time, error) {
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, err
	}
	return value.UTC(), nil
}
