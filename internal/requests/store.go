package requests

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
	"github.com/Felix-LeeSM/flick-drop/internal/usage"
)

type finalizeCall struct {
	done    chan struct{}
	receipt Receipt
	err     error
}

type Store struct {
	db     *sql.DB
	opts   Options
	now    func() time.Time
	copies chan struct{}
	// Coalesces concurrent finalizes of the same attempt into a single copy.
	finalizeMu sync.Mutex
	finalizing map[string]*finalizeCall
}

// Each copy spools ~256 MiB to /tmp; deploy/base/api.yaml sizes the emptyDir for this many.
const maxCopies = 2

type OutboxEnqueuer interface {
	EnqueueTx(context.Context, *sql.Tx, events.JobEvent) (events.OutboxRecord, error)
}

// Sizes are age file bytes, the unit size_bytes uses.
type Options struct {
	// Nil means object storage is disabled.
	Objects               *storage.Registry
	Outbox                OutboxEnqueuer
	PayloadInlineMaxBytes int64
	MaxObjectBytes        int64
	MinTTLSeconds         int
	DefaultTTLSeconds     int
	MaxTTLSeconds         int
}

func NewStore(db *sql.DB, opts Options) (*Store, error) {
	if db == nil || opts.PayloadInlineMaxBytes < int64(len(AgeMagic)) || opts.MaxObjectBytes <= 0 || (opts.Objects != nil && opts.Outbox == nil) || opts.MinTTLSeconds <= 0 || opts.DefaultTTLSeconds < opts.MinTTLSeconds || opts.DefaultTTLSeconds > opts.MaxTTLSeconds || int64(opts.MaxTTLSeconds) > int64((1<<63-1)/time.Second) {
		return nil, fmt.Errorf("invalid request store configuration")
	}
	return &Store{
		db:         db,
		opts:       opts,
		now:        func() time.Time { return time.Now().UTC() },
		copies:     make(chan struct{}, maxCopies),
		finalizing: make(map[string]*finalizeCall),
	}, nil
}

func (s *Store) SetNowForTest(now func() time.Time) { s.now = now }

type Created struct {
	ID              string    `json:"id"`
	ExpiresAt       time.Time `json:"expires_at"`
	SubmissionToken string    `json:"submission_token"`
	RetrievalToken  string    `json:"retrieval_token"`
}

type Instructions struct {
	PublicKey     string    `json:"public_key"`
	Fingerprint   string    `json:"fingerprint"`
	ExpiresAt     time.Time `json:"expires_at"`
	Generation    int       `json:"generation"`
	CanSubmit     bool      `json:"can_submit"`
	EncryptedNote string    `json:"encrypted_note,omitempty"`
}

type OwnerStatus struct {
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	ExpiresAt   time.Time `json:"expires_at"`
	State       string    `json:"state"`
}

type SubmitInput struct {
	Generation   int             `json:"generation"`
	AttemptToken string          `json:"attempt_token"`
	Kind         string          `json:"kind"`
	SizeBytes    int64           `json:"size_bytes"`
	Envelope     json.RawMessage `json:"envelope"`
	Ciphertext   string          `json:"ciphertext"`
}

type Receipt struct {
	Generation int    `json:"generation"`
	State      string `json:"state"`
}

type Payload struct {
	Kind       string          `json:"kind"`
	SizeBytes  int64           `json:"size_bytes"`
	Envelope   json.RawMessage `json:"envelope"`
	Ciphertext string          `json:"ciphertext"`
}

// The caller must close Body, then call FinishOpen when ObjectKey is set.
type Opened struct {
	Kind      string
	SizeBytes int64
	Envelope  Envelope
	Body      io.ReadCloser
	ObjectKey string
}

func (s *Store) Create(ctx context.Context, publicKey string, ttl *int, encryptedNote *string) (Created, error) {
	fingerprint, err := publicKeyFingerprint(publicKey)
	if err != nil {
		return Created{}, err
	}
	var note sql.NullString
	if encryptedNote != nil {
		validated, err := validateEncryptedNote(*encryptedNote)
		if err != nil {
			return Created{}, err
		}
		note = sql.NullString{String: validated, Valid: true}
	}
	seconds := s.opts.DefaultTTLSeconds
	if ttl != nil {
		seconds = *ttl
	}
	if seconds < s.opts.MinTTLSeconds || seconds > s.opts.MaxTTLSeconds {
		return Created{}, ErrInvalid
	}
	// Independent draws: neither capability derives from the ID or public key.
	var random [80]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Created{}, err
	}
	id := base64.RawURLEncoding.EncodeToString(random[:16])
	submit := base64.RawURLEncoding.EncodeToString(random[16:48])
	retrieve := base64.RawURLEncoding.EncodeToString(random[48:])
	submitHash, _ := tokenHash(submit)
	retrieveHash, _ := tokenHash(retrieve)
	expires := s.now().UTC().Add(time.Duration(seconds) * time.Second)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Created{}, fmt.Errorf("begin create request: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `insert into requests
		(id, public_key, fingerprint, encrypted_note, submission_token_hash, retrieval_token_hash, expires_at)
		values ($1, $2, $3, $4, $5, $6, $7)`, id, publicKey, fingerprint, note, submitHash, retrieveHash, expires)
	if err != nil {
		return Created{}, err
	}
	if err := usage.Increment(ctx, tx, s.now().UTC(), usage.RequestCreated, 1); err != nil {
		return Created{}, err
	}
	if err := tx.Commit(); err != nil {
		return Created{}, fmt.Errorf("commit create request: %w", err)
	}
	telemetry.RequestCreated.Inc()
	return Created{id, expires, submit, retrieve}, nil
}

type record struct {
	publicKey, fingerprint, encryptedNote, state string
	expires                                      time.Time
	generation                                   int
	reservationExpires                           time.Time
}

type rowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) authorized(ctx context.Context, db rowReader, id string, hash []byte, owner bool) (record, error) {
	column := "submission_token_hash"
	if owner {
		column = "retrieval_token_hash"
	}
	var row record
	var storedHash []byte
	var encryptedNote sql.NullString
	var reservationExpiry sql.NullTime
	err := db.QueryRowContext(ctx, `select public_key, fingerprint, encrypted_note, expires_at, state, generation, reservation_expires_at, `+column+` from requests where id = $1`, id).
		Scan(&row.publicKey, &row.fingerprint, &encryptedNote, &row.expires, &row.state, &row.generation, &reservationExpiry, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return record{}, ErrUnavailable
	}
	if err != nil {
		return record{}, err
	}
	if encryptedNote.Valid {
		row.encryptedNote = encryptedNote.String
	}
	if reservationExpiry.Valid {
		row.reservationExpires = reservationExpiry.Time
	}
	if subtle.ConstantTimeCompare(hash, storedHash) != 1 {
		return record{}, ErrUnavailable
	}
	if row.state == "unavailable" || !s.now().UTC().Before(row.expires) {
		return record{}, ErrUnavailable
	}
	return row, nil
}

func (s *Store) read(ctx context.Context, id, token string, owner bool) (record, error) {
	hash, err := tokenHash(token)
	if err != nil {
		return record{}, ErrUnavailable
	}
	row, err := s.authorized(ctx, s.db, id, hash, owner)
	if err == nil && row.state == "uploading" && !s.now().UTC().Before(row.reservationExpires) {
		tx, refreshed, refreshErr := s.begin(ctx, id, token, owner)
		if refreshErr != nil {
			return record{}, refreshErr
		}
		defer tx.close()
		if err := s.commit(tx, refreshed.expires); err != nil {
			return record{}, err
		}
		return refreshed, nil
	}
	return row, err
}

func (s *Store) Instructions(ctx context.Context, id, token string) (Instructions, error) {
	r, err := s.read(ctx, id, token, false)
	if err != nil {
		return Instructions{}, err
	}
	var encryptedNote string
	if r.state == "waiting" {
		encryptedNote = r.encryptedNote
	}
	return Instructions{r.publicKey, r.fingerprint, r.expires, r.generation, r.state == "waiting", encryptedNote}, nil
}

func (s *Store) Owner(ctx context.Context, id, token string) (OwnerStatus, error) {
	r, err := s.read(ctx, id, token, true)
	if err != nil {
		return OwnerStatus{}, err
	}
	return OwnerStatus{r.publicKey, r.fingerprint, r.expires, r.state}, nil
}

// Postgres rolls a failed COMMIT back server-side, so commit carries no manual rollback.
type writeTx struct {
	*sql.Tx
	conn *sql.Conn
}

func (tx *writeTx) close() {
	_ = tx.Rollback()
	_ = tx.conn.Close()
}

func (s *Store) begin(ctx context.Context, id, token string, owner bool) (*writeTx, record, error) {
	hash, err := tokenHash(token)
	if err != nil {
		return nil, record{}, ErrUnavailable
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, record{}, err
	}
	transaction, err := conn.BeginTx(ctx, nil)
	if err != nil {
		_ = conn.Close()
		return nil, record{}, err
	}
	tx := &writeTx{Tx: transaction, conn: conn}
	// The no-op UPDATE locks the row so a blocked writer re-reads it under READ COMMITTED.
	// Check the clock after the lock so a blocked writer cannot reuse a pre-expiry decision.
	column := "submission_token_hash"
	if owner {
		column = "retrieval_token_hash"
	}
	if _, err = tx.ExecContext(ctx, `update requests set state = state where id = $1 and `+column+` = $2`, id, hash); err != nil {
		tx.close()
		return nil, record{}, err
	}
	r, err := s.authorized(ctx, tx, id, hash, owner)
	if err != nil {
		tx.close()
		return nil, record{}, err
	}
	if r.state == "uploading" && !s.now().UTC().Before(r.reservationExpires) {
		if err := endReservationTx(ctx, tx.Tx, s.opts.Outbox, id, r.generation, s.now().UTC()); err != nil {
			tx.close()
			return nil, record{}, err
		}
		if err := s.commit(tx, r.expires); err != nil {
			tx.close()
			return nil, record{}, err
		}
		tx.close()
		return s.begin(ctx, id, token, owner)
	}
	return tx, r, nil
}

func (s *Store) commit(tx *writeTx, expires time.Time) error {
	if !expires.IsZero() && !s.now().UTC().Before(expires) {
		return ErrUnavailable
	}
	return tx.Commit()
}

func (s *Store) validateSubmission(in SubmitInput) ([]byte, []byte, []byte, error) {
	if in.Generation < 1 || in.Generation > MaxGeneration || in.SizeBytes < 0 || (in.Kind != "text" && in.Kind != "file") {
		return nil, nil, nil, ErrInvalid
	}
	attempt, err := tokenHash(in.AttemptToken)
	if err != nil {
		return nil, nil, nil, err
	}
	if in.SizeBytes > s.opts.PayloadInlineMaxBytes || (in.Kind == "file" && in.SizeBytes > s.opts.MaxObjectBytes) {
		return nil, nil, nil, ErrTooLarge
	}
	envelope, err := validateEnvelope(in.Envelope, in.Kind)
	if err != nil {
		return nil, nil, nil, err
	}
	// Body size is bounded at HTTP; domain callers receive the same limit.
	if int64(len(in.Ciphertext)) > (s.opts.PayloadInlineMaxBytes+2)/3*4 {
		return nil, nil, nil, ErrTooLarge
	}
	ciphertext, err := canonicalBase64(in.Ciphertext, int(in.SizeBytes), int(in.SizeBytes))
	if err != nil || !isAgeFile(ciphertext) {
		return nil, nil, nil, ErrInvalid
	}
	return attempt, envelope, ciphertext, nil
}

func (s *Store) Submit(ctx context.Context, id, token string, in SubmitInput) (Receipt, error) {
	attempt, envelope, ciphertext, err := s.validateSubmission(in)
	if err != nil {
		return Receipt{}, err
	}
	// Excludes token and JSON formatting. The digest survives payload deletion.
	immutable, _ := json.Marshal(Payload{in.Kind, in.SizeBytes, envelope, in.Ciphertext})
	bodyHash := sha256.Sum256(immutable)
	tx, row, err := s.begin(ctx, id, token, false)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.close()
	if in.Generation != row.generation {
		return Receipt{}, ErrUnavailable
	}
	if row.state == "uploading" {
		return Receipt{}, ErrConflict
	}
	if row.state != "waiting" {
		var storedAttempt, storedBody []byte
		if err := tx.QueryRowContext(ctx, `select attempt_token_hash, attempt_body_hash from requests where id = $1`, id).Scan(&storedAttempt, &storedBody); err != nil {
			return Receipt{}, err
		}
		if !bytes.Equal(attempt, storedAttempt) || !bytes.Equal(bodyHash[:], storedBody) {
			return Receipt{}, ErrConflict
		}
		if err := s.commit(tx, row.expires); err != nil {
			return Receipt{}, err
		}
		return Receipt{in.Generation, "submitted"}, nil
	}
	changed, err := tx.ExecContext(ctx, `update requests set state = 'submitted', attempt_token_hash = $1, attempt_body_hash = $2, kind = $3, size_bytes = $4, envelope_json = $5 where id = $6 and state = 'waiting' and generation = $7`, attempt, bodyHash[:], in.Kind, in.SizeBytes, string(envelope), id, in.Generation)
	if err != nil {
		return Receipt{}, err
	}
	if err := requireTransition(changed); err != nil {
		return Receipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `insert into request_payloads (request_id, ciphertext) values ($1, $2)`, id, ciphertext); err != nil {
		return Receipt{}, err
	}
	// Only waiting -> submitted counts; the replay branch must not double-count.
	if err := usage.Increment(ctx, tx.Tx, s.now().UTC(), usage.RequestSubmitted, 1); err != nil {
		return Receipt{}, err
	}
	if err := s.commit(tx, row.expires); err != nil {
		return Receipt{}, err
	}
	telemetry.RequestSubmitted.Inc()
	return Receipt{in.Generation, "submitted"}, nil
}

func (s *Store) Attempt(ctx context.Context, id, token string, generation int, attemptToken string) (Receipt, error) {
	if generation < 1 || generation > MaxGeneration {
		return Receipt{}, ErrInvalid
	}
	attempt, err := tokenHash(attemptToken)
	if err != nil {
		return Receipt{}, err
	}
	tx, row, err := s.begin(ctx, id, token, false)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.close()
	if generation != row.generation {
		return Receipt{}, ErrUnavailable
	}
	var stored []byte
	if err := tx.QueryRowContext(ctx, `select attempt_token_hash from requests where id = $1`, id).Scan(&stored); err != nil {
		return Receipt{}, err
	}
	state := "unavailable"
	if bytes.Equal(attempt, stored) {
		state = "accepted"
		if row.state == "uploading" {
			state = "uploading"
		}
	} else if row.state == "waiting" {
		state = "waiting"
	}
	if err := s.commit(tx, row.expires); err != nil {
		return Receipt{}, err
	}
	return Receipt{generation, state}, nil
}

// The S3 body opens before the consuming tx, so a storage failure leaves the request openable.
func (s *Store) Open(ctx context.Context, id, token string) (Opened, error) {
	tx, row, err := s.begin(ctx, id, token, true)
	if err != nil {
		return Opened{}, err
	}
	defer tx.close()
	if row.state != "submitted" {
		return Opened{}, ErrConflict
	}
	var opened Opened
	var envelope, backend string
	var key sql.NullString
	var ciphertext []byte
	if err := tx.QueryRowContext(ctx, `select r.kind, r.size_bytes, r.envelope_json, r.storage_backend, r.final_key, p.ciphertext from requests r left join request_payloads p on p.request_id = r.id where r.id = $1`, id).Scan(&opened.Kind, &opened.SizeBytes, &envelope, &backend, &key, &ciphertext); err != nil {
		return Opened{}, err
	}
	if err := json.Unmarshal([]byte(envelope), &opened.Envelope); err != nil {
		return Opened{}, err
	}
	if backend == db.StorageInline {
		if int64(len(ciphertext)) != opened.SizeBytes {
			return Opened{}, ErrStorage
		}
		if err := s.clearPayloadTx(ctx, tx.Tx, id, row.state, "consumed", events.ReasonConsumed); err != nil {
			return Opened{}, err
		}
		if err := usage.Increment(ctx, tx.Tx, s.now().UTC(), usage.RequestConsumed, 1); err != nil {
			return Opened{}, err
		}
		if err := s.commit(tx, row.expires); err != nil {
			return Opened{}, err
		}
		telemetry.RequestConsumed.Inc()
		opened.Body = io.NopCloser(bytes.NewReader(ciphertext))
		return opened, nil
	}

	// Object I/O must not hold the consuming tx's row lock; only the later conditional tx consumes.
	tx.close()
	if s.opts.Objects == nil {
		return Opened{}, ErrStorage
	}
	objects, err := s.opts.Objects.For(backend)
	if err != nil {
		return Opened{}, ErrStorage
	}
	if !key.Valid || opened.SizeBytes < 0 || opened.SizeBytes > s.opts.MaxObjectBytes {
		return Opened{}, ErrStorage
	}
	body, length, err := objects.Open(ctx, key.String)
	if err != nil {
		return Opened{}, ErrStorage
	}
	if length != opened.SizeBytes {
		body.Close()
		return Opened{}, ErrStorage
	}
	if err := s.consumeObject(ctx, id, token, key); err != nil {
		body.Close()
		return Opened{}, err
	}
	opened.Body = body
	opened.ObjectKey = key.String
	return opened, nil
}

func (s *Store) consumeObject(ctx context.Context, id, token string, key sql.NullString) error {
	tx, row, err := s.begin(ctx, id, token, true)
	if err != nil {
		return err
	}
	defer tx.close()
	if row.state != "submitted" {
		return ErrConflict
	}
	// final_key stays set to protect the object while the body streams.
	changed, err := tx.ExecContext(ctx, `update requests set state = 'consumed', consumed_at = $1, encrypted_note = null, kind = null, size_bytes = null, envelope_json = null, ciphertext_sha256 = null where id = $2 and state = 'submitted' and final_key = $3`, s.now().UTC(), id, key.String)
	if err != nil {
		return err
	}
	if err := requireTransition(changed); err != nil {
		return err
	}
	if err := usage.Increment(ctx, tx.Tx, s.now().UTC(), usage.RequestConsumed, 1); err != nil {
		return err
	}
	if err := s.commit(tx, row.expires); err != nil {
		return err
	}
	telemetry.RequestConsumed.Inc()
	return nil
}

// Safe to repeat. The consuming transition never clears storage_backend.
func (s *Store) FinishOpen(ctx context.Context, id, key string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	transaction, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	tx := &writeTx{Tx: transaction, conn: conn}
	defer tx.close()
	var backend string
	if err := tx.QueryRowContext(ctx, `select storage_backend from requests where id = $1`, id).Scan(&backend); err != nil {
		return err
	}
	changed, err := tx.ExecContext(ctx, `update requests set final_key = null where id = $1 and state = 'consumed' and final_key = $2`, id, key)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n == 0 {
		return err
	}
	if err := enqueueObjectTx(ctx, tx.Tx, s.opts.Outbox, backend, key, events.ReasonConsumed, s.now().UTC()); err != nil {
		return err
	}
	return s.commit(tx, time.Time{})
}

func (s *Store) Revoke(ctx context.Context, id, token string) error {
	tx, row, err := s.begin(ctx, id, token, true)
	if err != nil {
		return err
	}
	defer tx.close()
	if row.state == "consumed" {
		return ErrConflict
	}
	if err := s.clearPayloadTx(ctx, tx.Tx, id, row.state, "cancelled", events.ReasonManual); err != nil {
		return err
	}
	return s.commit(tx, row.expires)
}

func (s *Store) clearPayloadTx(ctx context.Context, tx *sql.Tx, id, previousState, state string, reason string) error {
	if err := enqueueRequestKeysTx(ctx, tx, s.opts.Outbox, id, reason, s.now().UTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from request_payloads where request_id = $1`, id); err != nil {
		return err
	}
	changed, err := tx.ExecContext(ctx, `update requests set state = $1, attempt_token_hash = case when state = 'uploading' then null else attempt_token_hash end, attempt_body_hash = case when state = 'uploading' then null else attempt_body_hash end, encrypted_note = null, kind = null, size_bytes = null, envelope_json = null, upload_key = null, final_key = null, reservation_expires_at = null, ciphertext_sha256 = null, consumed_at = case when $2 = 'consumed' then $3 else consumed_at end where id = $4 and state = $5`, state, state, s.now().UTC(), id, previousState)
	if err != nil {
		return err
	}
	return requireTransition(changed)
}

func requireTransition(result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
