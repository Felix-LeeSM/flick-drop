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
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
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
	uploadKey, finalKey         sql.NullString
	size                        sql.NullInt64
}

func objectAttemptTx(ctx context.Context, tx *sql.Tx, id string) (objectAttempt, error) {
	var a objectAttempt
	err := tx.QueryRowContext(ctx, `select attempt_token_hash, attempt_body_hash, ciphertext_sha256, upload_key, final_key, size_bytes from requests where id = ?`, id).Scan(&a.attempt, &a.bodyHash, &a.checksum, &a.uploadKey, &a.finalKey, &a.size)
	return a, err
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
	if in.Kind != "file" || in.SizeBytes < 0 {
		return UploadResult{}, ErrInvalid
	}
	if in.SizeBytes > s.opts.MaxFileBytes {
		return UploadResult{}, ErrTooLarge
	}
	if in.SizeBytes+tagBytes <= s.opts.PayloadInlineMaxBytes {
		return UploadResult{}, ErrInvalid
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
		uploadKey := ObjectPrefix + base64.RawURLEncoding.EncodeToString(random[:16]) + "/upload"
		finalKey := ObjectPrefix + base64.RawURLEncoding.EncodeToString(random[16:]) + "/final"
		deadline := s.now().UTC().Add(ReservationTTL)
		if row.expires.Before(deadline) {
			deadline = row.expires
		}
		result, err := tx.ExecContext(ctx, `update requests set state = 'uploading', storage_backend = 's3_object', attempt_token_hash = ?, attempt_body_hash = ?, kind = 'file', size_bytes = ?, envelope_json = ?, ciphertext_sha256 = ?, upload_key = ?, final_key = ?, reservation_expires_at = ? where id = ? and state = 'waiting' and generation = ?`, attempt, digest[:], in.SizeBytes, string(envelope), checksum, uploadKey, finalKey, deadline.Format(timeFormat), id, in.Generation)
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
	instruction, err := s.opts.Objects.PresignPUT(ctx, a.uploadKey.String, in.SizeBytes+tagBytes, ttl)
	if err != nil {
		return UploadResult{}, ErrStorage
	}
	// Bound the advertised deadline even if signing and response delivery take time.
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
		// Accepted receipts survive consume/revoke, but a cancelled reservation was
		// never accepted. It retains no attempt hash (see clearPayloadTx).
		if row.state != "submitted" && row.state != "consumed" && row.state != "cancelled" {
			return Receipt{}, ErrConflict
		}
		if err := s.commit(tx, row.expires); err != nil {
			return Receipt{}, err
		}
		return Receipt{row.generation, "submitted"}, nil
	}
	if s.opts.Objects == nil || !a.uploadKey.Valid || !a.finalKey.Valid || !a.size.Valid || a.size.Int64 < 0 || a.size.Int64 > s.opts.MaxFileBytes {
		return Receipt{}, ErrStorage
	}
	if err := s.commit(tx, row.expires); err != nil {
		return Receipt{}, err
	}
	tx.close()
	body, err := s.opts.Objects.GetBounded(ctx, a.uploadKey.String, a.size.Int64+tagBytes)
	if errors.Is(err, storage.ErrObjectTooLarge) {
		return Receipt{}, ErrInvalid
	}
	if err != nil {
		return Receipt{}, ErrStorage
	}
	if int64(len(body)) != a.size.Int64+tagBytes {
		return Receipt{}, ErrInvalid
	}
	checksum := sha256.Sum256(body)
	if !bytes.Equal(checksum[:], a.checksum) {
		return Receipt{}, ErrInvalid
	}
	// Same-attempt finalizers only write identical, checksum-verified bytes. Never
	// delete on error: a PUT may have succeeded, or another finalizer may accept.
	if err := s.opts.Objects.Put(ctx, a.finalKey.String, body); err != nil {
		return Receipt{}, ErrStorage
	}
	tx, row, err = s.begin(ctx, id, token, false)
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
	result, err := tx.ExecContext(ctx, `update requests set state = 'submitted', upload_key = null, reservation_expires_at = null where id = ? and state = 'uploading' and generation = ? and final_key = ?`, id, in.Generation, a.finalKey.String)
	if err != nil {
		return Receipt{}, err
	}
	if err := requireTransition(result); err != nil {
		return Receipt{}, err
	}
	if err := enqueueObjectTx(ctx, tx.Tx, s.opts.Outbox, a.uploadKey.String, events.ReasonOrphan, s.now().UTC()); err != nil {
		return Receipt{}, err
	}
	// Acceptance must still precede both deadlines after SQL/outbox work.
	if !s.now().UTC().Before(row.reservationExpires) {
		return Receipt{}, ErrUnavailable
	}
	if err := s.commit(tx, row.expires); err != nil {
		return Receipt{}, err
	}
	return Receipt{row.generation, "submitted"}, nil
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
