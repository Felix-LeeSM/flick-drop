package secrets

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
)

// Expiry values are pinned to UTC before they are written.
const managedObjectPrefix = "managed/secrets/"

var ErrManagementUnavailable = errors.New("management unavailable")
var ErrNotCancellable = errors.New("delivery is not cancellable")

type ManagementStatus struct {
	ID        string
	Status    string
	ExpiresAt time.Time
	CanCancel bool
}

func createManagementTx(ctx context.Context, tx *sql.Tx, id string, expires time.Time) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate management capability: %w", err)
	}
	hash := sha256.Sum256(raw[:])
	if _, err := tx.ExecContext(ctx, `insert into secret_management (secret_id, token_hash, expires_at) values ($1, $2, $3)`, id, hash[:], expires.UTC()); err != nil {
		return "", fmt.Errorf("insert management capability: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func recordManagementOutcomeTx(ctx context.Context, tx *sql.Tx, id, outcome string) error {
	_, err := tx.ExecContext(ctx, `update secret_management set outcome = $1 where secret_id = $2 and outcome is null`, outcome, id)
	if err != nil {
		return fmt.Errorf("record management outcome: %w", err)
	}
	return nil // Legacy secrets have no management record.
}

func (s *Store) Management(ctx context.Context, id, token string) (ManagementStatus, error) {
	return s.management(ctx, s.db, id, token)
}

func (s *Store) management(ctx context.Context, q queryer, id, token string) (ManagementStatus, error) {
	if len(token) != 43 {
		return ManagementStatus{}, ErrManagementUnavailable
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return ManagementStatus{}, ErrManagementUnavailable
	}
	var hash []byte
	var expires time.Time
	var outcome, state sql.NullString
	var created sql.NullTime
	err = q.QueryRowContext(ctx, `select m.token_hash, m.expires_at, m.outcome, s.state, s.created_at
		from secret_management m left join secrets s on s.id = m.secret_id where m.secret_id = $1`, id).
		Scan(&hash, &expires, &outcome, &state, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagementStatus{}, ErrManagementUnavailable
	}
	if err != nil {
		return ManagementStatus{}, fmt.Errorf("read management status: %w", err)
	}
	got := sha256.Sum256(raw)
	if subtle.ConstantTimeCompare(got[:], hash) != 1 {
		return ManagementStatus{}, ErrManagementUnavailable
	}
	now := s.now().UTC()
	if !now.Before(expires) {
		return ManagementStatus{}, ErrManagementUnavailable
	}
	status := outcome.String
	if !outcome.Valid {
		if !state.Valid {
			return ManagementStatus{}, fmt.Errorf("management outcome missing")
		}
		status = state.String
		if status == "pending_upload" {
			if !created.Valid {
				return ManagementStatus{}, fmt.Errorf("management upload time missing")
			}
			if !now.Before(created.Time.Add(s.pendingTTL)) {
				status = "unavailable"
			}
		}
	}
	return ManagementStatus{ID: id, Status: status, ExpiresAt: expires, CanCancel: status == "active" || status == "pending_upload"}, nil
}

// No network I/O occurs. A failed COMMIT aborts server-side, so no manual rollback is needed.
func (s *Store) Revoke(ctx context.Context, id, token string) (ManagementStatus, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagementStatus{}, fmt.Errorf("begin cancellation: %w", err)
	}
	defer rollback(tx)
	status, err := s.management(ctx, tx, id, token)
	if err != nil || status.Status == "cancelled" {
		return status, err
	}
	if !status.CanCancel {
		return status, ErrNotCancellable
	}
	// Under READ COMMITTED a concurrent open, lockout, or revoke can land between reads;
	// lostRace answers as if that winner had committed first.
	lostRace := func() (ManagementStatus, error) {
		status, err := s.management(ctx, tx, id, token)
		if err != nil || status.Status == "cancelled" {
			return status, err
		}
		return status, ErrNotCancellable
	}
	var backend, key string
	err = tx.QueryRowContext(ctx, `select storage_backend, storage_key from secrets where id = $1`, id).Scan(&backend, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return lostRace()
	}
	if err != nil {
		return ManagementStatus{}, fmt.Errorf("read cancellation storage: %w", err)
	}
	// Recheck after loading storage metadata so an elapsed deadline or concurrent commit wins over cancellation.
	status, err = s.management(ctx, tx, id, token)
	if err != nil {
		return ManagementStatus{}, err
	}
	if !status.CanCancel {
		return status, ErrNotCancellable
	}
	result, err := tx.ExecContext(ctx, `delete from secrets where id = $1 and consumed_at is null
		and reclaim_enqueued_at is null and state in ('active', 'pending_upload')`, id)
	if err != nil {
		return ManagementStatus{}, fmt.Errorf("cancel delivery: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return ManagementStatus{}, fmt.Errorf("read cancellation row count: %w", err)
	}
	if n != 1 {
		return lostRace()
	}
	if err := recordManagementOutcomeTx(ctx, tx, id, "cancelled"); err != nil {
		return ManagementStatus{}, err
	}
	if backend != StorageSQLite {
		if s.outbox == nil {
			return ManagementStatus{}, fmt.Errorf("cancellation outbox is required")
		}
		jobID, err := events.NewJobID()
		if err != nil {
			return ManagementStatus{}, err
		}
		if _, err := s.outbox.EnqueueTx(ctx, tx, events.JobEvent{JobID: jobID, Kind: events.KindDeleteOCIObject,
			Backend: backend, ObjectKey: key, Reason: events.ReasonManual, RequestedAt: s.now().UTC()}); err != nil {
			return ManagementStatus{}, fmt.Errorf("enqueue cancellation cleanup: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ManagementStatus{}, fmt.Errorf("commit cancellation: %w", err)
	}
	status.Status, status.CanCancel = "cancelled", false
	return status, nil
}

// Purge only expired managed terminal rows, whose deletion jobs committed with their outcome; leave legacy rows.
func purgeManagementTx(ctx context.Context, tx *sql.Tx, now time.Time, limit int) error {
	cutoff := now.UTC()
	const expired = `select m.secret_id from secret_management m
		where m.expires_at <= $1 order by m.expires_at, m.secret_id limit $2`
	if _, err := tx.ExecContext(ctx, `delete from secrets where consumed_at is not null and id in (`+expired+`)`, cutoff, limit); err != nil {
		return fmt.Errorf("purge managed terminal metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from secret_management where secret_id in (`+expired+`)`, cutoff, limit); err != nil {
		return fmt.Errorf("purge management capabilities: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from secret_open_grace where object_key in (
		select object_key from secret_open_grace where grace_until <= $1 order by grace_until limit $2)`, cutoff, limit); err != nil {
		return fmt.Errorf("purge streaming grace records: %w", err)
	}
	return nil
}
