package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	KindText        = "text"
	StorageSQLite   = "sqlite_blob"
	KDFPBKDF2SHA256 = "PBKDF2-SHA-256"
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
	EncryptedFilename *string
	ContentType       *string
	SizeBytes         int64
	ExpiresAt         time.Time
}

type Store struct {
	db                    *sql.DB
	now                   func() time.Time
	payloadInlineMaxBytes int64
	allowedTTLs           map[int]struct{}
}

type StoreOptions struct {
	PayloadInlineMaxBytes int64
	AllowedTTLSeconds     []int
}

func NewStore(db *sql.DB, opts StoreOptions) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("db is required")
	}
	if opts.PayloadInlineMaxBytes <= 0 {
		return nil, fmt.Errorf("payload inline max bytes must be positive")
	}
	if len(opts.AllowedTTLSeconds) == 0 {
		return nil, fmt.Errorf("allowed ttl seconds is required")
	}

	allowed := make(map[int]struct{}, len(opts.AllowedTTLSeconds))
	for _, ttl := range opts.AllowedTTLSeconds {
		if ttl <= 0 {
			return nil, fmt.Errorf("allowed ttl seconds must be positive")
		}
		allowed[ttl] = struct{}{}
	}

	return &Store{
		db:                    db,
		now:                   func() time.Time { return time.Now().UTC() },
		payloadInlineMaxBytes: opts.PayloadInlineMaxBytes,
		allowedTTLs:           allowed,
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
	kdfJSON, err := json.Marshal(input.KDF)
	if err != nil {
		return Secret{}, fmt.Errorf("marshal kdf params: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Secret{}, fmt.Errorf("begin create secret: %w", err)
	}
	defer rollback(tx)

	_, err = tx.ExecContext(ctx, `insert into secrets (
		id, kind, storage_backend, storage_key, nonce, kdf_algorithm, kdf_salt,
		kdf_params_json, encrypted_filename, content_type, size_bytes, max_views,
		view_count, expires_at, created_at, updated_at
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
		id,
		input.Kind,
		StorageSQLite,
		id,
		input.Nonce,
		input.KDF.Algorithm,
		input.KDF.Salt,
		string(kdfJSON),
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

func (s *Store) Consume(ctx context.Context, id string) error {
	now := s.now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin consume secret: %w", err)
	}
	defer rollback(tx)

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

	if _, err := tx.ExecContext(ctx, `delete from secret_payloads where secret_id = ?`, id); err != nil {
		return fmt.Errorf("delete consumed payload: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit consume secret: %w", err)
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
	var kdfJSON string
	var encryptedFilename sql.NullString
	var contentType sql.NullString
	var expiresRaw string
	var consumedAt sql.NullString

	err := q.QueryRowContext(ctx, `select
			s.id, s.kind, p.ciphertext, s.nonce, s.kdf_params_json,
			s.encrypted_filename, s.content_type, s.size_bytes, s.expires_at,
			s.consumed_at
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
		&encryptedFilename,
		&contentType,
		&secret.SizeBytes,
		&expiresRaw,
		&consumedAt,
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

	if err := json.Unmarshal([]byte(kdfJSON), &secret.KDF); err != nil {
		return Secret{}, sql.NullString{}, fmt.Errorf("decode kdf params: %w", err)
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
	if input.Kind != KindText {
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
	if _, ok := s.allowedTTLs[input.TTLSeconds]; !ok {
		return ErrInvalidInput
	}
	if input.MaxViews != 1 {
		return ErrUnsupportedViews
	}
	if input.KDF.Algorithm != KDFPBKDF2SHA256 || input.KDF.Salt == "" || input.KDF.Iterations < 600000 || input.KDF.KeyLengthBits != 256 {
		return ErrInvalidInput
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
