package requests

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Fixed-width UTC timestamps preserve indexed subsecond deadline ordering.
const timeFormat = "2006-01-02T15:04:05.000000000Z"

type Store struct {
	db   *sql.DB
	opts Options
	now  func() time.Time
}

type Options struct {
	PayloadInlineMaxBytes int64
	MaxFileBytes          int64
	MinTTLSeconds         int
	DefaultTTLSeconds     int
	MaxTTLSeconds         int
}

func NewStore(db *sql.DB, opts Options) (*Store, error) {
	if db == nil || opts.PayloadInlineMaxBytes <= tagBytes || opts.MaxFileBytes <= 0 || opts.MinTTLSeconds <= 0 || opts.DefaultTTLSeconds < opts.MinTTLSeconds || opts.DefaultTTLSeconds > opts.MaxTTLSeconds || int64(opts.MaxTTLSeconds) > int64((1<<63-1)/time.Second) {
		return nil, fmt.Errorf("invalid request store configuration")
	}
	return &Store{db: db, opts: opts, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Store) SetNowForTest(now func() time.Time) { s.now = now }

type Created struct {
	ID              string    `json:"id"`
	ExpiresAt       time.Time `json:"expires_at"`
	SubmissionToken string    `json:"submission_token"`
	RetrievalToken  string    `json:"retrieval_token"`
}

type Instructions struct {
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	ExpiresAt   time.Time `json:"expires_at"`
	Generation  int       `json:"generation"`
	CanSubmit   bool      `json:"can_submit"`
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

func (s *Store) Create(ctx context.Context, publicKey string, ttl *int) (Created, error) {
	fingerprint, err := publicKeyFingerprint(publicKey)
	if err != nil {
		return Created{}, err
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
	_, err = s.db.ExecContext(ctx, `insert into requests
		(id, public_key, fingerprint, submission_token_hash, retrieval_token_hash, expires_at)
		values (?, ?, ?, ?, ?, ?)`, id, publicKey, fingerprint, submitHash, retrieveHash, expires.Format(timeFormat))
	if err != nil {
		return Created{}, err
	}
	return Created{id, expires, submit, retrieve}, nil
}

type record struct {
	publicKey, fingerprint, state string
	expires                       time.Time
	generation                    int
}

// Both DB and transactions expose QueryRowContext; the helper keeps read and
// write authorization/deadline checks identical without reading any payload.
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
	var expiry string
	err := db.QueryRowContext(ctx, `select public_key, fingerprint, expires_at, state, generation, `+column+` from requests where id = ?`, id).
		Scan(&row.publicKey, &row.fingerprint, &expiry, &row.state, &row.generation, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return record{}, ErrUnavailable
	}
	if err != nil {
		return record{}, err
	}
	if subtle.ConstantTimeCompare(hash, storedHash) != 1 {
		return record{}, ErrUnavailable
	}
	row.expires, err = time.Parse(timeFormat, expiry)
	if err != nil {
		return record{}, err
	}
	if !s.now().UTC().Before(row.expires) {
		return record{}, ErrUnavailable
	}
	return row, nil
}

func (s *Store) read(ctx context.Context, id, token string, owner bool) (record, error) {
	hash, err := tokenHash(token)
	if err != nil {
		return record{}, ErrUnavailable
	}
	return s.authorized(ctx, s.db, id, hash, owner)
}

func (s *Store) Instructions(ctx context.Context, id, token string) (Instructions, error) {
	r, err := s.read(ctx, id, token, false)
	if err != nil {
		return Instructions{}, err
	}
	return Instructions{r.publicKey, r.fingerprint, r.expires, r.generation, r.state == "waiting"}, nil
}

func (s *Store) Owner(ctx context.Context, id, token string) (OwnerStatus, error) {
	r, err := s.read(ctx, id, token, true)
	if err != nil {
		return OwnerStatus{}, err
	}
	return OwnerStatus{r.publicKey, r.fingerprint, r.expires, r.state}, nil
}

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
	// Acquire the SQLite writer before reading. A deferred read-then-write can
	// fail with SQLITE_BUSY_SNAPSHOT across API connections. Check the clock
	// after the lock, so a blocked writer cannot reuse a pre-expiry decision.
	column := "submission_token_hash"
	if owner {
		column = "retrieval_token_hash"
	}
	if _, err = tx.ExecContext(ctx, `update requests set state = state where id = ? and `+column+` = ?`, id, hash); err != nil {
		tx.close()
		return nil, record{}, err
	}
	r, err := s.authorized(ctx, tx, id, hash, owner)
	if err != nil {
		tx.close()
		return nil, record{}, err
	}
	return tx, r, nil
}

func (s *Store) commit(tx *writeTx, expires time.Time) error {
	if !s.now().UTC().Before(expires) {
		return ErrUnavailable
	}
	err := tx.Commit()
	if err != nil {
		// SQLite can leave the transaction active after a failed COMMIT, while
		// database/sql marks Tx done. Pin the connection until explicit rollback
		// completes, so a pooled connection cannot expose uncommitted state.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, rollbackErr := tx.conn.ExecContext(ctx, "rollback"); rollbackErr != nil {
			// A cancelled driver transaction may already be rolled back. Discard
			// conservatively if its state cannot be established.
			_ = tx.conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}
	return err
}

func (s *Store) validateSubmission(in SubmitInput) ([]byte, []byte, []byte, error) {
	if in.Generation < 1 || in.Generation > MaxGeneration || in.SizeBytes < 0 || (in.Kind != "text" && in.Kind != "file") {
		return nil, nil, nil, ErrInvalid
	}
	attempt, err := tokenHash(in.AttemptToken)
	if err != nil {
		return nil, nil, nil, err
	}
	if in.SizeBytes > s.opts.PayloadInlineMaxBytes-tagBytes || (in.Kind == "file" && in.SizeBytes > s.opts.MaxFileBytes) {
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
	ciphertext, err := canonicalBase64(in.Ciphertext, int(in.SizeBytes)+tagBytes, int(in.SizeBytes)+tagBytes)
	if err != nil {
		return nil, nil, nil, err
	}
	return attempt, envelope, ciphertext, nil
}

func (s *Store) Submit(ctx context.Context, id, token string, in SubmitInput) (Receipt, error) {
	attempt, envelope, ciphertext, err := s.validateSubmission(in)
	if err != nil {
		return Receipt{}, err
	}
	// Hash canonical immutable fields, excluding token and JSON formatting. The
	// generation is independently fenced. The digest survives payload deletion.
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
	if row.state != "waiting" {
		var storedAttempt, storedBody []byte
		if err := tx.QueryRowContext(ctx, `select attempt_token_hash, attempt_body_hash from requests where id = ?`, id).Scan(&storedAttempt, &storedBody); err != nil {
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
	changed, err := tx.ExecContext(ctx, `update requests set state = 'submitted', attempt_token_hash = ?, attempt_body_hash = ?, kind = ?, size_bytes = ?, envelope_json = ? where id = ? and state = 'waiting' and generation = ?`, attempt, bodyHash[:], in.Kind, in.SizeBytes, string(envelope), id, in.Generation)
	if err != nil {
		return Receipt{}, err
	}
	if err := requireTransition(changed); err != nil {
		return Receipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `insert into request_payloads (request_id, ciphertext) values (?, ?)`, id, ciphertext); err != nil {
		return Receipt{}, err
	}
	if err := s.commit(tx, row.expires); err != nil {
		return Receipt{}, err
	}
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
	hash, err := tokenHash(token)
	if err != nil {
		return Receipt{}, ErrUnavailable
	}
	// One read transaction gives the capability, state, and receipt one snapshot.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback()
	row, err := s.authorized(ctx, tx, id, hash, false)
	if err != nil {
		return Receipt{}, err
	}
	if generation != row.generation {
		return Receipt{}, ErrUnavailable
	}
	var stored []byte
	if err := tx.QueryRowContext(ctx, `select attempt_token_hash from requests where id = ?`, id).Scan(&stored); err != nil {
		return Receipt{}, err
	}
	state := "unavailable"
	if bytes.Equal(attempt, stored) {
		state = "accepted"
	} else if row.state == "waiting" {
		state = "waiting"
	}
	if !s.now().UTC().Before(row.expires) {
		return Receipt{}, ErrUnavailable
	}
	return Receipt{generation, state}, nil
}

func (s *Store) Open(ctx context.Context, id, token string) (Payload, error) {
	tx, row, err := s.begin(ctx, id, token, true)
	if err != nil {
		return Payload{}, err
	}
	defer tx.close()
	if row.state != "submitted" {
		return Payload{}, ErrConflict
	}
	var payload Payload
	var envelope string
	var ciphertext []byte
	if err := tx.QueryRowContext(ctx, `select r.kind, r.size_bytes, r.envelope_json, p.ciphertext from requests r join request_payloads p on p.request_id = r.id where r.id = ?`, id).Scan(&payload.Kind, &payload.SizeBytes, &envelope, &ciphertext); err != nil {
		return Payload{}, err
	}
	payload.Envelope = json.RawMessage(envelope)
	payload.Ciphertext = base64.StdEncoding.EncodeToString(ciphertext)
	if err := clearPayloadTx(ctx, tx.Tx, id, row.state, "consumed"); err != nil {
		return Payload{}, err
	}
	if err := s.commit(tx, row.expires); err != nil {
		return Payload{}, err
	}
	return payload, nil
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
	if err := clearPayloadTx(ctx, tx.Tx, id, row.state, "cancelled"); err != nil {
		return err
	}
	return s.commit(tx, row.expires)
}

func clearPayloadTx(ctx context.Context, tx *sql.Tx, id, previousState, state string) error {
	if _, err := tx.ExecContext(ctx, `delete from request_payloads where request_id = ?`, id); err != nil {
		return err
	}
	changed, err := tx.ExecContext(ctx, `update requests set state = ?, kind = null, size_bytes = null, envelope_json = null where id = ? and state = ?`, state, id, previousState)
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

// PurgeExpiredTx runs in the existing API reaper transaction. Inline ciphertext
// cascades; no worker job is needed because no object remains outside api.db.
func PurgeExpiredTx(ctx context.Context, tx *sql.Tx, now time.Time, limit int) error {
	_, err := tx.ExecContext(ctx, `delete from requests where id in (select id from requests where expires_at <= ? order by expires_at, id limit ?)`, now.UTC().Format(timeFormat), limit)
	return err
}
