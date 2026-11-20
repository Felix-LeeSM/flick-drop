package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
	"github.com/Felix-LeeSM/flick-drop/internal/usage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Span attributes carry only safe metadata (kind, storage backend), never ciphertext, identities, proofs, or keys.
var tracer = otel.Tracer("github.com/Felix-LeeSM/flick-drop/internal/secrets")

const (
	KindText                = "text"
	KindFile                = "file"
	StorageSQLite           = db.StorageInline
	KDFPBKDF2SHA256         = "PBKDF2-SHA-256"
	maxFailedAccessAttempts = 5
)

// A CounterVec child exists only after its first Inc; without this the series vanishes after restart (#374).
func registerCreatedSeries(objects *storage.Registry) {
	backends := []string{StorageSQLite}
	if objects != nil {
		backends = append(backends, objects.Names()...)
	}
	for _, kind := range []string{KindText, KindFile} {
		for _, backend := range backends {
			telemetry.SecretCreated.WithLabelValues(kind, backend)
		}
	}
}

type KDFParams struct {
	Algorithm     string `json:"algorithm"`
	Salt          string `json:"salt"`
	Iterations    int    `json:"iterations"`
	KeyLengthBits int    `json:"key_length_bits"`
}

type CreateInput struct {
	Kind              string
	Ciphertext        []byte
	AccessKDF         KDFParams
	AccessProofHash   string
	WrappedIdentity   *string
	EncryptedFilename *string
	ContentType       *string
	SizeBytes         int64
	TTLSeconds        int
	MaxViews          int
}

type CreateLargeInput struct {
	Kind              string
	AccessKDF         KDFParams
	AccessProofHash   string
	WrappedIdentity   *string
	EncryptedFilename *string
	ContentType       *string
	SizeBytes         int64
	TTLSeconds        int
	MaxViews          int
}

type CreateLargeResult struct {
	ManagementToken string
	ID              string
	ExpiresAt       time.Time
	Upload          storage.UploadInstruction
}

type Secret struct {
	// Returned only by Create; never loaded from storage or returned on open.
	ManagementToken string `json:"-"`
	ID              string
	Kind            string
	StorageBackend  string
	StorageKey      string
	// An S3 body opened by OpenTx arrives in Body instead; the caller must close it.
	Ciphertext        []byte
	Body              io.ReadCloser
	AccessKDF         KDFParams
	WrappedIdentity   *string
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
	objects               *storage.Registry
	outbox                outboxEnqueuer
}

type StoreOptions struct {
	PayloadInlineMaxBytes int64
	MaxObjectBytes        int64
	PresignTTL            time.Duration
	PendingTTL            time.Duration
	MinTTLSeconds         int
	MaxTTLSeconds         int
	Objects               *storage.Registry
	// Without one (tests only), the lockout falls back to a best-effort inline delete.
	Outbox outboxEnqueuer
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

	registerCreatedSeries(opts.Objects)
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
		outbox:                opts.Outbox,
	}, nil
}

func (s *Store) SetNowForTest(now func() time.Time) {
	s.now = now
}

func (s *Store) Create(ctx context.Context, input CreateInput) (_ Secret, err error) {
	ctx, span := tracer.Start(ctx, "secrets.Create", trace.WithAttributes(
		attribute.String("secret.kind", input.Kind),
		attribute.String("storage.backend", StorageSQLite),
	))
	defer func() { telemetry.EndSpan(span, err) }()

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

	accessKDFParamsJSON, accessProofHash, err := modelAColumns(input.AccessKDF, input.AccessProofHash)
	if err != nil {
		return Secret{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Secret{}, fmt.Errorf("begin create secret: %w", err)
	}
	defer rollback(tx)

	_, err = tx.ExecContext(ctx, `insert into secrets (
		id, kind, storage_backend, storage_key, access_kdf_params_json,
		access_proof_hash, wrapped_identity, encrypted_filename, content_type,
		size_bytes, max_views, view_count, expires_at, created_at, updated_at
	) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 0, $12, $13, $14)`,
		id,
		input.Kind,
		StorageSQLite,
		id,
		accessKDFParamsJSON,
		accessProofHash,
		input.WrappedIdentity,
		input.EncryptedFilename,
		input.ContentType,
		input.SizeBytes,
		input.MaxViews,
		expiresAt,
		now,
		now,
	)
	if err != nil {
		return Secret{}, fmt.Errorf("insert secret metadata: %w", err)
	}

	_, err = tx.ExecContext(ctx, `insert into secret_payloads (
		secret_id, ciphertext, created_at
	) values ($1, $2, $3)`, id, input.Ciphertext, now)
	if err != nil {
		return Secret{}, fmt.Errorf("insert secret payload: %w", err)
	}

	managementToken, err := createManagementTx(ctx, tx, id, expiresAt)
	if err != nil {
		return Secret{}, err
	}

	if err := usage.Increment(ctx, tx, now, usage.SecretCreatedMetric(input.Kind), 1); err != nil {
		return Secret{}, err
	}
	// Large uploads count bytes in Finalize, where the object is confirmed.
	if err := usage.Increment(ctx, tx, now, usage.BytesUploaded, input.SizeBytes); err != nil {
		return Secret{}, err
	}

	if err := tx.Commit(); err != nil {
		return Secret{}, fmt.Errorf("commit create secret: %w", err)
	}

	telemetry.SecretCreated.WithLabelValues(input.Kind, StorageSQLite).Inc()

	return Secret{
		ManagementToken:   managementToken,
		ID:                id,
		Kind:              input.Kind,
		StorageBackend:    StorageSQLite,
		StorageKey:        id,
		Ciphertext:        append([]byte(nil), input.Ciphertext...),
		AccessKDF:         input.AccessKDF,
		WrappedIdentity:   input.WrappedIdentity,
		EncryptedFilename: input.EncryptedFilename,
		ContentType:       input.ContentType,
		SizeBytes:         input.SizeBytes,
		ExpiresAt:         expiresAt,
	}, nil
}

func (s *Store) CreateLarge(ctx context.Context, input CreateLargeInput) (_ CreateLargeResult, err error) {
	ctx, span := tracer.Start(ctx, "secrets.CreateLarge", trace.WithAttributes(
		attribute.String("secret.kind", input.Kind),
	))
	defer func() { telemetry.EndSpan(span, err) }()

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

	accessKDFParamsJSON, accessProofHash, err := modelAColumns(input.AccessKDF, input.AccessProofHash)
	if err != nil {
		return CreateLargeResult{}, err
	}

	// The usage read runs outside a tx, so concurrent placements can transiently overshoot a ceiling
	// (the documented soft cap). A rejection inserts no row and presigns nothing.
	backendUsage, err := db.StorageObjectBytes(ctx, s.db, s.now().UTC(), s.objects.Names())
	if err != nil {
		return CreateLargeResult{}, fmt.Errorf("read object usage: %w", err)
	}
	backend := s.objects.Choose(backendUsage, input.SizeBytes)
	if backend == "" {
		return CreateLargeResult{}, ErrStorageFull
	}
	objects, err := s.objects.For(backend)
	if err != nil {
		return CreateLargeResult{}, fmt.Errorf("resolve placement backend: %w", err)
	}
	span.SetAttributes(attribute.String("storage.backend", backend))
	storageKey := managedObjectPrefix + id
	upload, err := objects.PresignPUT(ctx, storageKey, input.SizeBytes, s.presignTTL)
	if err != nil {
		return CreateLargeResult{}, fmt.Errorf("presign upload: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreateLargeResult{}, fmt.Errorf("begin create large secret: %w", err)
	}
	defer rollback(tx)

	_, err = tx.ExecContext(ctx, `insert into secrets (
		id, kind, storage_backend, storage_key, access_kdf_params_json,
		access_proof_hash, wrapped_identity, encrypted_filename, content_type,
		size_bytes, max_views, view_count, state, expires_at, created_at, updated_at
	) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 0, 'pending_upload', $12, $13, $14)`,
		id,
		input.Kind,
		backend,
		storageKey,
		accessKDFParamsJSON,
		accessProofHash,
		input.WrappedIdentity,
		input.EncryptedFilename,
		input.ContentType,
		input.SizeBytes,
		input.MaxViews,
		expiresAt,
		now,
		now,
	)
	if err != nil {
		return CreateLargeResult{}, fmt.Errorf("insert large secret metadata: %w", err)
	}
	managementToken, err := createManagementTx(ctx, tx, id, expiresAt)
	if err != nil {
		return CreateLargeResult{}, err
	}
	// bytes_uploaded waits for Finalize so an abandoned presigned upload never inflates it.
	if err := usage.Increment(ctx, tx, now, usage.SecretCreatedMetric(input.Kind), 1); err != nil {
		return CreateLargeResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreateLargeResult{}, fmt.Errorf("commit create large secret: %w", err)
	}

	telemetry.SecretCreated.WithLabelValues(input.Kind, backend).Inc()

	return CreateLargeResult{ID: id, ExpiresAt: expiresAt, Upload: upload, ManagementToken: managementToken}, nil
}

// Idempotent. The SELECT takes no row lock, so the bucket HEAD never blocks a concurrent Revoke or open.
func (s *Store) Finalize(ctx context.Context, id string) (err error) {
	ctx, span := tracer.Start(ctx, "secrets.Finalize")
	defer func() { telemetry.EndSpan(span, err) }()

	if s.objects == nil {
		return ErrPayloadTooLarge
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finalize: %w", err)
	}
	defer rollback(tx)

	var state, storageBackend, storageKey string
	var expiresAt, createdAt time.Time
	var sizeBytes int64
	err = tx.QueryRowContext(ctx, `select state, storage_backend, storage_key, expires_at, created_at, size_bytes from secrets where id = $1`, id).
		Scan(&state, &storageBackend, &storageKey, &expiresAt, &createdAt, &sizeBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load pending secret: %w", err)
	}
	span.SetAttributes(attribute.String("storage.backend", storageBackend))
	if state == "active" {
		return nil
	}
	if state != "pending_upload" {
		return ErrNotPending
	}
	deadline := expiresAt
	if pendingDeadline := createdAt.Add(s.pendingTTL); pendingDeadline.Before(deadline) {
		deadline = pendingDeadline
	}
	if !s.now().UTC().Before(deadline) {
		return ErrExpired
	}

	objects, err := s.objects.For(storageBackend)
	if err != nil {
		return fmt.Errorf("resolve finalize backend: %w", err)
	}
	info, err := objects.Head(ctx, storageKey)
	if err != nil {
		return fmt.Errorf("head uploaded object: %w", err)
	}
	// The bucket should already enforce the signed length; check anyway so activation never depends on it.
	if !info.Exists || info.Size != sizeBytes {
		return ErrObjectMissing
	}

	now := s.now().UTC()
	if !now.Before(deadline) {
		return ErrExpired
	}
	activated, err := s.activateSecretTx(ctx, tx, id, now)
	if err != nil {
		return err
	}
	if !activated {
		// The winning replica already counted the bytes; returning without incrementing avoids double-counting.
		return nil
	}
	if err := usage.Increment(ctx, tx, now, usage.BytesUploaded, sizeBytes); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit finalize: %w", err)
	}
	return nil
}

// 0 rows means reaped, revoked, reaper-claimed, or already activated by another replica;
// the re-read treats already-active as idempotent success and anything else as ErrNotFound.
func (s *Store) activateSecretTx(ctx context.Context, tx *sql.Tx, id string, now time.Time) (bool, error) {
	// Once a reaper claimed the orphan and enqueued its delete, finalize must not activate it.
	result, err := tx.ExecContext(ctx, `update secrets set state='active', updated_at=$1
		where id=$2 and state='pending_upload' and reclaim_enqueued_at is null`, now, id)
	if err != nil {
		return false, fmt.Errorf("activate secret: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read activate row count: %w", err)
	}
	if affected != 1 {
		// Under READ COMMITTED this re-read sees the concurrent commit that beat this UPDATE.
		var state string
		err := tx.QueryRowContext(ctx, `select state from secrets where id = $1`, id).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, fmt.Errorf("re-read secret state after activate: %w", err)
		}
		if state == "active" {
			return false, nil
		}
		return false, ErrNotFound
	}
	return true, nil
}

func (s *Store) PendingTTL() time.Duration { return s.pendingTTL }

func (s *Store) CountPendingUploads(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `select count(*) from secrets where state = 'pending_upload'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count pending uploads: %w", err)
	}
	return n, nil
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
	var consumedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `select
			s.id, s.kind, s.access_kdf_params_json, s.size_bytes, s.expires_at, s.consumed_at
		from secrets s
		where s.id = $1 and (s.storage_backend <> 'sqlite_blob'
			or exists (select 1 from secret_payloads p where p.secret_id = s.id))`,
		id,
	).Scan(
		&metadata.ID,
		&metadata.Kind,
		&accessKDFJSON,
		&metadata.SizeBytes,
		&metadata.ExpiresAt,
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
	if accessKDFJSON.Valid {
		if err := json.Unmarshal([]byte(accessKDFJSON.String), &metadata.AccessKDF); err != nil {
			return Metadata{}, fmt.Errorf("decode access kdf params: %w", err)
		}
	}
	if !s.now().UTC().Before(metadata.ExpiresAt) {
		return Metadata{}, ErrExpired
	}
	return metadata, nil
}

func (s *Store) OpenTx(ctx context.Context, tx *sql.Tx, id string, accessProofHash string) (_ Secret, err error) {
	ctx, span := tracer.Start(ctx, "secrets.OpenTx")
	defer func() { telemetry.EndSpan(span, err) }()

	if tx == nil {
		return Secret{}, fmt.Errorf("transaction is required")
	}

	now := s.now().UTC()
	secret, consumedAt, err := s.load(ctx, tx, id)
	if err != nil {
		return Secret{}, err
	}
	span.SetAttributes(attribute.String("storage.backend", secret.StorageBackend))
	if consumedAt.Valid {
		return Secret{}, ErrConsumed
	}
	if !now.Before(secret.ExpiresAt) {
		return Secret{}, ErrExpired
	}

	// Model B carries no proof: the link is the capability. max_views 1 limits a captured key to one open.
	if secret.accessProofHash != "" {
		if subtle.ConstantTimeCompare([]byte(secret.accessProofHash), []byte(accessProofHash)) != 1 {
			if err := s.recordFailedAccessTx(ctx, tx, id, secret.StorageBackend, secret.StorageKey, now); err != nil {
				return Secret{}, err
			}
			return Secret{}, ErrInvalidAccess
		}
	}

	// Open the body before marking consumed so an unservable bucket leaves the secret reopenable.
	if err := s.openBody(ctx, &secret); err != nil {
		return Secret{}, err
	}
	defer func() {
		if err != nil && secret.Body != nil {
			secret.Body.Close()
		}
	}()

	now = s.now().UTC()
	if !now.Before(secret.ExpiresAt) {
		return Secret{}, ErrExpired
	}

	// Opened means forgotten: every later request for the ID gets the unknown-ID response.
	result, err := tx.ExecContext(ctx, `delete from secrets where id = $1 and consumed_at is null`, id)
	if err != nil {
		return Secret{}, fmt.Errorf("delete opened secret: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Secret{}, fmt.Errorf("read open row count: %w", err)
	}
	if affected != 1 {
		return Secret{}, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `delete from secret_management where secret_id = $1`, id); err != nil {
		return Secret{}, fmt.Errorf("delete opened management record: %w", err)
	}
	// With the row gone, only this record keeps reconciliation away until the stream deadline passes.
	if secret.Body != nil {
		if _, err := tx.ExecContext(ctx, `insert into secret_open_grace (object_key, grace_until) values ($1, $2)`,
			secret.StorageKey, now.Add(openObjectGrace)); err != nil {
			return Secret{}, fmt.Errorf("protect streaming object: %w", err)
		}
	}
	if err := usage.Increment(ctx, tx, now, usage.SecretOpened, 1); err != nil {
		return Secret{}, err
	}
	return secret, nil
}

// The caller announces the /finalize-checked length before streaming, so it must match exactly.
func (s *Store) openBody(ctx context.Context, secret *Secret) error {
	if secret.StorageBackend == StorageSQLite {
		return nil
	}
	if s.objects == nil {
		return ErrNotFound
	}
	objects, err := s.objects.For(secret.StorageBackend)
	if err != nil {
		return fmt.Errorf("open object payload: %w", err)
	}
	body, size, err := objects.Open(ctx, secret.StorageKey)
	if err != nil {
		return fmt.Errorf("open object payload: %w", err)
	}
	if size != secret.SizeBytes {
		body.Close()
		return fmt.Errorf("open object payload: object is %d bytes, want %d", size, secret.SizeBytes)
	}
	secret.Body = body
	return nil
}

func (s *Store) recordFailedAccessTx(ctx context.Context, tx *sql.Tx, id string, storageBackend, storageKey string, now time.Time) error {
	// One atomic statement so concurrent final attempts cannot both read a pre-lockout count.
	var failedAccessCount int
	var consumedAt sql.NullTime
	err := tx.QueryRowContext(ctx, `update secrets
		set failed_access_count = failed_access_count + 1,
			consumed_at = case
				when failed_access_count + 1 >= $2 then $3
				else consumed_at
			end,
			updated_at = $3
		where id = $1 and consumed_at is null
		returning failed_access_count, consumed_at`,
		id,
		maxFailedAccessAttempts,
		now,
	).Scan(&failedAccessCount, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// 0 rows: a concurrent final attempt locked it, or a concurrent open or revoke deleted it.
		var exists bool
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from secrets where id = $1)`, id).Scan(&exists); err != nil {
			return fmt.Errorf("re-read secret after failed access: %w", err)
		}
		if !exists {
			return ErrNotFound
		}
		return ErrConsumed
	}
	if err != nil {
		return fmt.Errorf("record failed secret access: %w", err)
	}
	if err := usage.Increment(ctx, tx, now, usage.SecretOpenFailed, 1); err != nil {
		return err
	}

	if failedAccessCount >= maxFailedAccessAttempts && consumedAt.Valid {
		if err := recordManagementOutcomeTx(ctx, tx, id, "locked"); err != nil {
			return err
		}
		// Enqueue the object delete in this tx: an inline S3 call would hold row locks across network I/O
		// and leak the object on a transient failure.
		switch {
		case storageBackend == StorageSQLite:
			if _, err := tx.ExecContext(ctx, `delete from secret_payloads where secret_id = $1`, id); err != nil {
				return fmt.Errorf("delete locked secret payload: %w", err)
			}
		case s.outbox != nil:
			jobID, err := events.NewJobID()
			if err != nil {
				return fmt.Errorf("generate locked object delete job id: %w", err)
			}
			if _, err := s.outbox.EnqueueTx(ctx, tx, events.JobEvent{
				JobID:       jobID,
				Kind:        events.KindDeleteOCIObject,
				Backend:     storageBackend,
				ObjectKey:   storageKey,
				Reason:      events.ReasonConsumed,
				RequestedAt: now,
			}); err != nil {
				return fmt.Errorf("enqueue locked object delete: %w", err)
			}
		case s.objects != nil:
			// A failed delete must not roll back the lockout count, which would gift the attacker another attempt.
			objects, err := s.objects.For(storageBackend)
			if err == nil {
				_ = objects.Delete(ctx, storageKey)
			}
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

	result, err := tx.ExecContext(ctx, `delete from secret_payloads where secret_id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("delete secret payload: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read cleanup row count: %w", err)
	}
	if affected > 0 {
		if _, err := tx.ExecContext(ctx, `update secrets set updated_at = $1 where id = $2`, now, id); err != nil {
			return false, fmt.Errorf("mark secret cleaned: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit cleanup secret: %w", err)
	}
	return affected > 0, nil
}

// Only rows the reaper has claimed can be reaped. A missing row is not an error.
func (s *Store) ReclaimTx(ctx context.Context, tx *sql.Tx, id string) error {
	if id == "" {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `delete from secret_payloads where secret_id = $1`, id); err != nil {
		return fmt.Errorf("delete secret payload on reclaim: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from secrets where id = $1 and reclaim_enqueued_at is not null`, id); err != nil {
		return fmt.Errorf("delete secret on reclaim: %w", err)
	}
	return nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) load(ctx context.Context, q queryer, id string) (Secret, sql.NullTime, error) {
	if id == "" {
		return Secret{}, sql.NullTime{}, ErrNotFound
	}

	var secret Secret
	var ciphertext []byte
	var accessKDFJSON sql.NullString
	var accessProofHash sql.NullString
	var wrappedIdentity sql.NullString
	var encryptedFilename sql.NullString
	var contentType sql.NullString
	var storageBackend sql.NullString
	var storageKey sql.NullString
	var consumedAt sql.NullTime
	var failedAccessCount int

	err := q.QueryRowContext(ctx, `select
			s.id, s.kind, s.storage_backend, s.storage_key, p.ciphertext,
			s.access_kdf_params_json, s.access_proof_hash, s.wrapped_identity,
			s.encrypted_filename, s.content_type, s.size_bytes, s.expires_at,
			s.consumed_at, s.failed_access_count
		from secrets s
		left join secret_payloads p on p.secret_id = s.id
		where s.id = $1 and s.state = 'active'`,
		id,
	).Scan(
		&secret.ID,
		&secret.Kind,
		&storageBackend,
		&storageKey,
		&ciphertext,
		&accessKDFJSON,
		&accessProofHash,
		&wrappedIdentity,
		&encryptedFilename,
		&contentType,
		&secret.SizeBytes,
		&secret.ExpiresAt,
		&consumedAt,
		&failedAccessCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, sql.NullTime{}, ErrNotFound
	}
	if err != nil {
		return Secret{}, sql.NullTime{}, fmt.Errorf("load secret: %w", err)
	}
	secret.StorageBackend = storageBackend.String
	secret.StorageKey = storageKey.String
	if secret.StorageBackend == StorageSQLite && len(ciphertext) == 0 && !consumedAt.Valid {
		return Secret{}, sql.NullTime{}, ErrNotFound
	}
	secret.Ciphertext = ciphertext

	// The table CHECK enforces Model A/B columns; a mix here is corruption.
	if accessKDFJSON.Valid != accessProofHash.Valid || accessProofHash.Valid != wrappedIdentity.Valid {
		return Secret{}, sql.NullTime{}, ErrInvalidInput
	}
	if accessKDFJSON.Valid {
		if err := json.Unmarshal([]byte(accessKDFJSON.String), &secret.AccessKDF); err != nil {
			return Secret{}, sql.NullTime{}, fmt.Errorf("decode access kdf params: %w", err)
		}
		secret.accessProofHash = accessProofHash.String
		secret.WrappedIdentity = &wrappedIdentity.String
	}

	if encryptedFilename.Valid {
		secret.EncryptedFilename = &encryptedFilename.String
	}
	if contentType.Valid {
		secret.ContentType = &contentType.String
	}

	return secret, consumedAt, nil
}

func (s *Store) validateCreate(input CreateInput) error {
	if int64(len(input.Ciphertext)) > s.payloadInlineMaxBytes {
		return ErrPayloadTooLarge
	}
	if !bytes.HasPrefix(input.Ciphertext, ageMagic) || int64(len(input.Ciphertext)) != input.SizeBytes {
		return ErrInvalidInput
	}
	return s.validateCommon(input.Kind, input.TTLSeconds, input.MaxViews, input.AccessKDF, input.AccessProofHash, input.WrappedIdentity, input.EncryptedFilename, input.ContentType)
}

func (s *Store) validateCreateLarge(input CreateLargeInput) error {
	if input.Kind == KindText {
		return ErrInvalidInput
	}
	// The signature pins this length, so an oversized declaration must be refused before signing.
	if input.SizeBytes > s.maxObjectBytes {
		return ErrPayloadTooLarge
	}
	if input.SizeBytes < MaxCiphertextBytes(0)-AgeHeaderAllowanceBytes {
		return ErrInvalidInput
	}
	return s.validateCommon(input.Kind, input.TTLSeconds, input.MaxViews, input.AccessKDF, input.AccessProofHash, input.WrappedIdentity, input.EncryptedFilename, input.ContentType)
}

func (s *Store) validateCommon(kind string, ttlSeconds, maxViews int, accessKDF KDFParams, accessProofHash string, wrappedIdentity, encryptedFilename, contentType *string) error {
	if kind != KindText && kind != KindFile {
		return ErrUnsupportedKind
	}
	if ttlSeconds < s.minTTL || ttlSeconds > s.maxTTL {
		return ErrInvalidInput
	}
	if maxViews != 1 {
		return ErrUnsupportedViews
	}

	// A secret is either Model A (all three present) or Model B (all absent), never a mix.
	hasProof := accessProofHash != ""
	hasAccessKDF := accessKDF.Algorithm != ""
	hasWrapped := wrappedIdentity != nil
	if hasProof != hasAccessKDF || hasProof != hasWrapped {
		return ErrInvalidInput
	}
	if hasProof {
		if accessKDF.Algorithm != KDFPBKDF2SHA256 || accessKDF.Salt == "" || accessKDF.Iterations < 600000 || accessKDF.Iterations > 6000000 || accessKDF.KeyLengthBits != 256 {
			return ErrInvalidInput
		}
		if _, err := decodeAgeField(*wrappedIdentity, maxWrappedIdentityBytes); err != nil {
			return ErrInvalidInput
		}
	}
	switch {
	case kind == KindFile && encryptedFilename == nil:
		return ErrInvalidInput
	case kind == KindText && encryptedFilename != nil:
		return ErrInvalidInput
	}
	if encryptedFilename != nil {
		if _, err := decodeAgeField(*encryptedFilename, maxEncryptedFilenameBytes); err != nil {
			return ErrInvalidInput
		}
	}
	if contentType != nil && !validContentType(*contentType) {
		return ErrInvalidInput
	}
	return nil
}

// Printable ASCII only: sent in Flick-Content-Type after the secret is consumed, so a bad
// value would break the header and make the recipient's fetch fail on a burned secret.
func validContentType(value string) bool {
	if len(value) > maxContentTypeBytes {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func modelAColumns(accessKDF KDFParams, accessProofHash string) (any, any, error) {
	if accessProofHash == "" {
		return nil, nil, nil
	}
	accessKDFJSON, err := json.Marshal(accessKDF)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal access kdf params: %w", err)
	}
	return string(accessKDFJSON), accessProofHash, nil
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
