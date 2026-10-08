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
)

// Fixed-width UTC timestamps make the new table's indexed expiry comparison
// exact even when one timestamp has no fractional seconds.
const managementTimeFormat = "2006-01-02T15:04:05.000000000Z"
const managedObjectPrefix = "managed/secrets/"

var ErrManagementUnavailable = errors.New("management unavailable")

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
	err = s.db.QueryRowContext(ctx, `select m.token_hash, m.expires_at, m.outcome, s.state, s.created_at
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
