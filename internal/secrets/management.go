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
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
)

// Fixed-width UTC timestamps make the new table's indexed expiry comparison
// exact even when one timestamp has no fractional seconds.
const managementTimeFormat = "2006-01-02T15:04:05.000000000Z"
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
	if _, err := tx.ExecContext(ctx, `insert into secret_management (secret_id, token_hash, expires_at) values (?, ?, ?)`, id, hash[:], expires.Format(managementTimeFormat)); err != nil {
		return "", fmt.Errorf("insert management capability: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func recordManagementOutcomeTx(ctx context.Context, tx *sql.Tx, id, outcome string) error {
	_, err := tx.ExecContext(ctx, `update secret_management set outcome = ? where secret_id = ? and outcome is null`, outcome, id)
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
	var expiresRaw string
	var outcome, state, createdRaw sql.NullString
	err = q.QueryRowContext(ctx, `select m.token_hash, m.expires_at, m.outcome, s.state, s.created_at
		from secret_management m left join secrets s on s.id = m.secret_id where m.secret_id = ?`, id).
		Scan(&hash, &expiresRaw, &outcome, &state, &createdRaw)
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
	expires, err := parseTime(expiresRaw)
	if err != nil {
		return ManagementStatus{}, fmt.Errorf("read management expiry: %w", err)
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
			created, err := parseTime(createdRaw.String)
			if err != nil {
				return ManagementStatus{}, fmt.Errorf("read management upload time: %w", err)
			}
			if !now.Before(created.Add(s.pendingTTL)) {
				status = "unavailable"
			}
		}
	}
	return ManagementStatus{ID: id, Status: status, ExpiresAt: expires, CanCancel: status == "active" || status == "pending_upload"}, nil
}

// Revoke retains only the authenticated outcome, removing access and payload in
// the same transaction as any required object cleanup. No network I/O occurs.
func (s *Store) Revoke(ctx context.Context, id, token string) (ManagementStatus, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return ManagementStatus{}, fmt.Errorf("acquire cancellation connection: %w", err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
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
	var backend, key string
	if err := tx.QueryRowContext(ctx, `select storage_backend, storage_key from secrets where id = ?`, id).Scan(&backend, &key); err != nil {
		return ManagementStatus{}, fmt.Errorf("read cancellation storage: %w", err)
	}
	// Recheck after loading storage metadata so an elapsed pending/content
	// deadline wins over cancellation, even if the transaction waited.
	status, err = s.management(ctx, tx, id, token)
	if err != nil {
		return ManagementStatus{}, err
	}
	if !status.CanCancel {
		return status, ErrNotCancellable
	}
	result, err := tx.ExecContext(ctx, `delete from secrets where id = ? and consumed_at is null
		and reclaim_enqueued_at is null and state in ('active', 'pending_upload')`, id)
	if err != nil {
		return ManagementStatus{}, fmt.Errorf("cancel delivery: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ManagementStatus{}, fmt.Errorf("cancellation did not claim a live delivery")
	}
	if err := recordManagementOutcomeTx(ctx, tx, id, "cancelled"); err != nil {
		return ManagementStatus{}, err
	}
	if backend == StorageS3 {
		if s.outbox == nil {
			return ManagementStatus{}, fmt.Errorf("cancellation outbox is required")
		}
		jobID, err := events.NewJobID()
		if err != nil {
			return ManagementStatus{}, err
		}
		if _, err := s.outbox.EnqueueTx(ctx, tx, events.JobEvent{JobID: jobID, Kind: events.KindDeleteOCIObject,
			ObjectKey: key, Reason: events.ReasonManual, RequestedAt: s.now().UTC()}); err != nil {
			return ManagementStatus{}, fmt.Errorf("enqueue cancellation cleanup: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		// SQLite may leave a failed COMMIT transaction open (for example a
		// deferred constraint). sql.Tx is already done, so roll back on the
		// still-exclusively-owned connection before returning it to the pool.
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "rollback")
		return ManagementStatus{}, fmt.Errorf("commit cancellation: %w", err)
	}
	if status.Status == "pending_upload" {
		telemetry.ActiveUploads.Dec()
	}
	status.Status, status.CanCancel = "cancelled", false
	return status, nil
}

// Payload cleanup remains independent. Purge only expired managed terminal rows,
// whose deletion jobs were committed with their outcome; leave legacy rows alone.
func purgeManagementTx(ctx context.Context, tx *sql.Tx, now time.Time, limit int) error {
	cutoff := now.Format(managementTimeFormat)
	if _, err := tx.ExecContext(ctx, `delete from secrets where consumed_at is not null and id in
		(select secret_id from secret_management where expires_at <= ? order by expires_at, secret_id limit ?)`, cutoff, limit); err != nil {
		return fmt.Errorf("purge managed terminal metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from secret_management where secret_id in
		(select secret_id from secret_management where expires_at <= ? order by expires_at, secret_id limit ?)`, cutoff, limit); err != nil {
		return fmt.Errorf("purge management capabilities: %w", err)
	}
	return nil
}
