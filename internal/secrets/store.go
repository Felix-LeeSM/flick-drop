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
)

const (
	KindText                = "text"
	KindFile                = "file"
	StorageSQLite           = "sqlite_blob"
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

type Secret struct {
	ID                string
	Kind              string
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
	minTTL                int
	maxTTL                int
}

type StoreOptions struct {
	PayloadInlineMaxBytes int64
	MinTTLSeconds         int
	MaxTTLSeconds         int
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

	return &Store{
		db:                    db,
		now:                   func() time.Time { return time.Now().UTC() },
		payloadInlineMaxBytes: opts.PayloadInlineMaxBytes,
		minTTL:                opts.MinTTLSeconds,
		maxTTL:                opts.MaxTTLSeconds,
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
		join secret_payloads p on p.secret_id = s.id
		where s.id = ?`,
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
			if err := s.recordFailedAccessTx(ctx, tx, id, now); err != nil {
				return Secret{}, err
			}
			return Secret{}, ErrInvalidAccess
		}
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

func (s *Store) recordFailedAccessTx(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
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
		if _, err := tx.ExecContext(ctx, `delete from secret_payloads where secret_id = ?`, id); err != nil {
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

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) load(ctx context.Context, q queryer, id string) (Secret, sql.NullString, error) {
	if id == "" {
		return Secret{}, sql.NullString{}, ErrNotFound
	}

	var secret Secret
	var kdfJSON sql.NullString
	var accessKDFJSON sql.NullString
	var accessProofHash sql.NullString
	var encryptedFilename sql.NullString
	var contentType sql.NullString
	var expiresRaw string
	var consumedAt sql.NullString
	var failedAccessCount int

	err := q.QueryRowContext(ctx, `select
			s.id, s.kind, p.ciphertext, s.nonce, s.kdf_params_json,
			s.access_kdf_params_json, s.access_proof_hash,
			s.encrypted_filename, s.content_type, s.size_bytes, s.expires_at,
			s.consumed_at, s.failed_access_count
		from secrets s
		left join secret_payloads p on p.secret_id = s.id
		where s.id = ?`,
		id,
	).Scan(
		&secret.ID,
		&secret.Kind,
		&secret.Ciphertext,
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
	if len(secret.Ciphertext) == 0 && !consumedAt.Valid {
		return Secret{}, sql.NullString{}, ErrNotFound
	}

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
