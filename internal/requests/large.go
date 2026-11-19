package requests

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
	"github.com/Felix-LeeSM/flick-drop/internal/usage"
)

const ObjectPrefix = "managed/requests/"
const ReservationTTL = 15 * time.Minute

type UploadInput struct {
	Generation       int             `json:"generation"`
	AttemptToken     string          `json:"attempt_token"`
	Kind             string          `json:"kind"`
	SizeBytes        int64           `json:"size_bytes"`
	Envelope         json.RawMessage `json:"envelope"`
	CiphertextSHA256 string          `json:"ciphertext_sha256"`
}

type AttemptInput struct {
	Generation   int    `json:"generation"`
	AttemptToken string `json:"attempt_token"`
}

type UploadResult struct {
	Generation           int                        `json:"generation"`
	State                string                     `json:"state"`
	Upload               *storage.UploadInstruction `json:"upload,omitempty"`
	ReservationExpiresAt *time.Time                 `json:"reservation_expires_at,omitempty"`
}

type objectAttempt struct {
	attempt, bodyHash, checksum []byte
	backend                     sql.NullString
	uploadKey, finalKey         sql.NullString
	size                        sql.NullInt64
}

func objectAttemptTx(ctx context.Context, tx *sql.Tx, id string) (objectAttempt, error) {
	var a objectAttempt
	err := tx.QueryRowContext(ctx, `select attempt_token_hash, attempt_body_hash, ciphertext_sha256, storage_backend, upload_key, final_key, size_bytes from requests where id = $1`, id).Scan(&a.attempt, &a.bodyHash, &a.checksum, &a.backend, &a.uploadKey, &a.finalKey, &a.size)
	return a, err
}

func backendOf(a objectAttempt) string {
	return a.backend.String
}

func validateAttempt(in AttemptInput) ([]byte, error) {
	if in.Generation < 1 || in.Generation > MaxGeneration {
		return nil, ErrInvalid
	}
	return tokenHash(in.AttemptToken)
}

func (s *Store) Reserve(ctx context.Context, id, token string, in UploadInput) (UploadResult, error) {
	attempt, err := validateAttempt(AttemptInput{in.Generation, in.AttemptToken})
	if err != nil {
		return UploadResult{}, err
	}
	// Clients route on the pre-encryption inline limit, so any file may take the large path.
	if in.Kind != "file" || in.SizeBytes < int64(len(AgeMagic)) {
		return UploadResult{}, ErrInvalid
	}
	if in.SizeBytes > s.opts.MaxObjectBytes {
		return UploadResult{}, ErrTooLarge
	}
	envelope, err := validateEnvelope(in.Envelope, in.Kind)
	if err != nil {
		return UploadResult{}, err
	}
	checksum, err := canonicalBase64(in.CiphertextSHA256, sha256.Size, sha256.Size)
	if err != nil {
		return UploadResult{}, err
	}
	immutable, _ := json.Marshal(struct {
		Kind     string          `json:"kind"`
		Size     int64           `json:"size_bytes"`
		Envelope json.RawMessage `json:"envelope"`
		Checksum string          `json:"ciphertext_sha256"`
	}{in.Kind, in.SizeBytes, envelope, in.CiphertextSHA256})
	digest := sha256.Sum256(immutable)
	tx, row, err := s.begin(ctx, id, token, false)
	if err != nil {
		return UploadResult{}, err
	}
	defer tx.close()
	if row.generation != in.Generation {
		return UploadResult{}, ErrUnavailable
	}
	if row.state != "waiting" {
		a, err := objectAttemptTx(ctx, tx.Tx, id)
		if err != nil {
			return UploadResult{}, err
		}
		if !bytes.Equal(attempt, a.attempt) || !bytes.Equal(digest[:], a.bodyHash) {
			return UploadResult{}, ErrConflict
		}
		if row.state != "uploading" {
			if err := s.commit(tx, row.expires); err != nil {
				return UploadResult{}, err
			}
			return UploadResult{Generation: row.generation, State: "submitted"}, nil
		}
	} else {
		if s.opts.Objects == nil {
			return UploadResult{}, ErrStorage
		}
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			return UploadResult{}, err
		}
		// Usage is read in the reservation tx so the ceiling check and the row share one commit scope.
		usage, err := db.StorageObjectBytes(ctx, tx.Tx, s.now().UTC(), s.opts.Objects.Names())
		if err != nil {
			return UploadResult{}, ErrStorage
		}
		backend := s.opts.Objects.Choose(usage, in.SizeBytes)
		if backend == "" {
			return UploadResult{}, ErrStorageFull
		}
		uploadKey := ObjectPrefix + base64.RawURLEncoding.EncodeToString(random[:16]) + "/upload"
		finalKey := ObjectPrefix + base64.RawURLEncoding.EncodeToString(random[16:]) + "/final"
		deadline := s.now().UTC().Add(ReservationTTL)
		if row.expires.Before(deadline) {
			deadline = row.expires
		}
		result, err := tx.ExecContext(ctx, `update requests set state = 'uploading', storage_backend = $1, attempt_token_hash = $2, attempt_body_hash = $3, kind = 'file', size_bytes = $4, envelope_json = $5, ciphertext_sha256 = $6, upload_key = $7, final_key = $8, reservation_expires_at = $9 where id = $10 and state = 'waiting' and generation = $11`, backend, attempt, digest[:], in.SizeBytes, string(envelope), checksum, uploadKey, finalKey, deadline, id, in.Generation)
		if err != nil {
			return UploadResult{}, err
		}
		if err := requireTransition(result); err != nil {
			return UploadResult{}, err
		}
		row.reservationExpires = deadline
	}
	a, err := objectAttemptTx(ctx, tx.Tx, id)
	if err != nil {
		return UploadResult{}, err
	}
	if s.opts.Objects == nil || !a.uploadKey.Valid {
		return UploadResult{}, ErrStorage
	}
	if err := s.commit(tx, row.expires); err != nil {
		return UploadResult{}, err
	}
	tx.close()
	ttl := row.reservationExpires.Sub(s.now().UTC())
	if ttl <= 0 {
		return UploadResult{}, ErrUnavailable
	}
	objects, err := s.opts.Objects.For(backendOf(a))
	if err != nil {
		return UploadResult{}, ErrStorage
	}
	instruction, err := objects.PresignPUT(ctx, a.uploadKey.String, in.SizeBytes, ttl)
	if err != nil {
		return UploadResult{}, ErrStorage
	}
	instruction.ExpiresAt = row.reservationExpires
	return UploadResult{Generation: row.generation, State: "uploading", Upload: &instruction, ReservationExpiresAt: &row.reservationExpires}, nil
}

func (s *Store) Finalize(ctx context.Context, id, token string, in AttemptInput) (Receipt, error) {
	attempt, err := validateAttempt(in)
	if err != nil {
		return Receipt{}, err
	}
	tx, row, err := s.begin(ctx, id, token, false)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.close()
	a, err := objectAttemptTx(ctx, tx.Tx, id)
	if err != nil {
		return Receipt{}, err
	}
	if in.Generation != row.generation {
		return Receipt{}, ErrUnavailable
	}
	if !bytes.Equal(attempt, a.attempt) {
		return Receipt{}, ErrConflict
	}
	if row.state != "uploading" {
		// A cancelled reservation was never accepted and retains no attempt hash.
		if row.state != "submitted" && row.state != "consumed" && row.state != "cancelled" {
			return Receipt{}, ErrConflict
		}
		if err := s.commit(tx, row.expires); err != nil {
			return Receipt{}, err
		}
		return Receipt{row.generation, "submitted"}, nil
	}
	if s.opts.Objects == nil || !a.uploadKey.Valid || !a.finalKey.Valid || !a.size.Valid || a.size.Int64 < 0 || a.size.Int64 > s.opts.MaxObjectBytes {
		return Receipt{}, ErrStorage
	}
	if err := s.commit(tx, row.expires); err != nil {
		return Receipt{}, err
	}
	tx.close()

	s.finalizeMu.Lock()
	if call, inFlight := s.finalizing[id]; inFlight {
		s.finalizeMu.Unlock()
		select {
		case <-call.done:
			return call.receipt, call.err
		case <-ctx.Done():
			return Receipt{}, ctx.Err()
		}
	}
	call := &finalizeCall{done: make(chan struct{})}
	s.finalizing[id] = call
	s.finalizeMu.Unlock()

	defer func() {
		s.finalizeMu.Lock()
		delete(s.finalizing, id)
		close(call.done)
		s.finalizeMu.Unlock()
	}()

	receipt, err := s.finalizeAttempt(ctx, id, token, in, a, attempt)
	call.receipt = receipt
	call.err = err
	return receipt, err
}

func (s *Store) finalizeAttempt(ctx context.Context, id, token string, in AttemptInput, a objectAttempt, attempt []byte) (Receipt, error) {
	if err := s.copyVerified(ctx, a); err != nil {
		return Receipt{}, err
	}
	tx, row, err := s.begin(ctx, id, token, false)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.close()
	current, err := objectAttemptTx(ctx, tx.Tx, id)
	if err != nil {
		return Receipt{}, err
	}
	if in.Generation != row.generation {
		return Receipt{}, ErrUnavailable
	}
	if !bytes.Equal(attempt, current.attempt) {
		return Receipt{}, ErrConflict
	}
	if row.state != "uploading" {
		if row.state != "submitted" && row.state != "consumed" && row.state != "cancelled" {
			return Receipt{}, ErrConflict
		}
		if err := s.commit(tx, row.expires); err != nil {
			return Receipt{}, err
		}
		return Receipt{row.generation, "submitted"}, nil
	}
	if current.finalKey != a.finalKey || !bytes.Equal(current.checksum, a.checksum) {
		return Receipt{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `update requests set state = 'submitted', upload_key = null, reservation_expires_at = null where id = $1 and state = 'uploading' and generation = $2 and final_key = $3`, id, in.Generation, a.finalKey.String)
	if err != nil {
		return Receipt{}, err
	}
	if err := requireTransition(result); err != nil {
		return Receipt{}, err
	}
	// Only uploading -> submitted counts; a losing or replayed finalize never reaches here.
	if err := usage.Increment(ctx, tx.Tx, s.now().UTC(), usage.RequestSubmitted, 1); err != nil {
		return Receipt{}, err
	}
	if err := enqueueObjectTx(ctx, tx.Tx, s.opts.Outbox, backendOf(a), a.uploadKey.String, events.ReasonOrphan, s.now().UTC()); err != nil {
		return Receipt{}, err
	}
	// Acceptance must still precede both deadlines after SQL/outbox work.
	if !s.now().UTC().Before(row.reservationExpires) {
		return Receipt{}, ErrUnavailable
	}
	if err := s.commit(tx, row.expires); err != nil {
		return Receipt{}, err
	}
	telemetry.RequestSubmitted.Inc()
	return Receipt{row.generation, "submitted"}, nil
}

// Bytes are verified before PutStream writes, so a losing finalizer cannot write a re-PUT upload.
// Never delete on error: a write may have succeeded; reconciliation removes unused final objects.
func (s *Store) copyVerified(ctx context.Context, a objectAttempt) error {
	select {
	case s.copies <- struct{}{}:
		defer func() { <-s.copies }()
	case <-ctx.Done():
		return ErrStorage
	}
	objects, err := s.opts.Objects.ForRequest(backendOf(a))
	if err != nil {
		return ErrStorage
	}
	body, length, err := objects.Open(ctx, a.uploadKey.String)
	if err != nil {
		return ErrStorage
	}
	defer body.Close()
	if length != a.size.Int64 {
		return ErrInvalid
	}
	hash := sha256.New()
	head := &prefixRecorder{limit: len(AgeMagic)}
	src := io.TeeReader(io.LimitReader(body, length), io.MultiWriter(hash, head))
	verify := func() error {
		if !isAgeFile(head.b) || !bytes.Equal(hash.Sum(nil), a.checksum) {
			return ErrInvalid
		}
		return nil
	}
	if err := objects.PutStream(ctx, a.finalKey.String, src, length, verify); err != nil {
		if errors.Is(err, ErrInvalid) {
			return ErrInvalid
		}
		return ErrStorage
	}
	return nil
}

type prefixRecorder struct {
	b     []byte
	limit int
}

func (p *prefixRecorder) Write(b []byte) (int, error) {
	if room := p.limit - len(p.b); room > 0 {
		p.b = append(p.b, b[:min(room, len(b))]...)
	}
	return len(b), nil
}

func (s *Store) Abandon(ctx context.Context, id, token string, in AttemptInput) (Receipt, error) {
	attempt, err := validateAttempt(in)
	if err != nil {
		return Receipt{}, err
	}
	tx, row, err := s.begin(ctx, id, token, false)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.close()
	if row.generation != in.Generation {
		return Receipt{}, ErrUnavailable
	}
	a, err := objectAttemptTx(ctx, tx.Tx, id)
	if err != nil {
		return Receipt{}, err
	}
	if row.state != "uploading" || !bytes.Equal(attempt, a.attempt) {
		return Receipt{}, ErrConflict
	}
	if err := endReservationTx(ctx, tx.Tx, s.opts.Outbox, id, row.generation, s.now().UTC()); err != nil {
		return Receipt{}, err
	}
	if err := s.commit(tx, row.expires); err != nil {
		return Receipt{}, err
	}
	if row.generation == MaxGeneration {
		return Receipt{}, ErrUnavailable
	}
	return Receipt{row.generation + 1, "waiting"}, nil
}
