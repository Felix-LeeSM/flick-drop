package requests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

func enqueueObjectTx(ctx context.Context, tx *sql.Tx, outbox OutboxEnqueuer, key, reason string, now time.Time) error {
	if !strings.HasPrefix(key, ObjectPrefix) || key == ObjectPrefix || outbox == nil {
		return ErrStorage
	}
	jobID, err := events.NewJobID()
	if err != nil {
		return err
	}
	_, err = outbox.EnqueueTx(ctx, tx, events.JobEvent{JobID: jobID, Kind: events.KindDeleteOCIObject, ObjectKey: key, Reason: reason, RequestedAt: now})
	return err
}

func enqueueRequestKeysTx(ctx context.Context, tx *sql.Tx, outbox OutboxEnqueuer, id, reason string, now time.Time) error {
	var upload, final sql.NullString
	if err := tx.QueryRowContext(ctx, `select upload_key, final_key from requests where id = ?`, id).Scan(&upload, &final); err != nil {
		return err
	}
	for _, key := range []sql.NullString{upload, final} {
		if key.Valid {
			if err := enqueueObjectTx(ctx, tx, outbox, key.String, reason, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// The transition fences the former generation permanently before its cleanup
// can commit. Generation exhaustion does not wrap or allow another submission.
func endReservationTx(ctx context.Context, tx *sql.Tx, outbox OutboxEnqueuer, id string, generation int, now time.Time) error {
	if err := enqueueRequestKeysTx(ctx, tx, outbox, id, events.ReasonOrphan, now); err != nil {
		return err
	}
	next, state := generation+1, "waiting"
	if generation == MaxGeneration {
		next, state = generation, "unavailable"
	}
	changed, err := tx.ExecContext(ctx, `update requests set state = ?, generation = ?, storage_backend = 'sqlite_blob', attempt_token_hash = null, attempt_body_hash = null, kind = null, size_bytes = null, envelope_json = null, upload_key = null, final_key = null, ciphertext_sha256 = null, reservation_expires_at = null where id = ? and state = 'uploading' and generation = ?`, state, next, id, generation)
	if err != nil {
		return err
	}
	return requireTransition(changed)
}

// PurgeExpiredTx shares the existing API reaper transaction. Both request expiry
// and reservation expiry have independent bounded batches; outbox failure rolls
// back metadata deletion and generation invalidation together.
func PurgeExpiredTx(ctx context.Context, tx *sql.Tx, now time.Time, limit int, outboxes ...OutboxEnqueuer) error {
	var outbox OutboxEnqueuer
	if len(outboxes) != 0 {
		outbox = outboxes[0]
	}
	rows, err := tx.QueryContext(ctx, `select id, generation from requests where state = 'uploading' and reservation_expires_at <= ? and expires_at > ? order by reservation_expires_at, id limit ?`, now.UTC().Format(timeFormat), now.UTC().Format(timeFormat), limit)
	if err != nil {
		return err
	}
	type candidate struct {
		id         string
		generation int
	}
	var pending []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.generation); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range pending {
		if err := endReservationTx(ctx, tx, outbox, c.id, c.generation, now); err != nil {
			return err
		}
	}
	rows, err = tx.QueryContext(ctx, `select id from requests where expires_at <= ? order by expires_at, id limit ?`, now.UTC().Format(timeFormat), limit)
	if err != nil {
		return err
	}
	var expired []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range expired {
		if err := enqueueRequestKeysTx(ctx, tx, outbox, id, events.ReasonExpired, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `delete from requests where id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileOnce owns only managed/requests/. Cursor and per-key pending claims
// survive restarts and are independent of managed/secrets/ listing progress.
func (s *Store) ReconcileOnce(ctx context.Context, limit int) (int, error) {
	if s.opts.Objects == nil {
		return 0, nil
	}
	if limit < 1 {
		return 0, errors.New("invalid request reconciliation batch")
	}
	limit = min(limit, 1000)
	var cursor string
	var generation int64
	if err := s.db.QueryRowContext(ctx, `select continuation_token, generation from request_reconciliation_cursor where id = 1`).Scan(&cursor, &generation); err != nil {
		return 0, err
	}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	page, err := s.opts.Objects.List(listCtx, ObjectPrefix, cursor, limit)
	cancel()
	if err != nil {
		if errors.Is(err, storage.ErrInvalidCursor) && cursor != "" {
			if _, resetErr := s.db.ExecContext(ctx, `update request_reconciliation_cursor set continuation_token = '', generation = generation + 1 where id = 1 and generation = ?`, generation); resetErr != nil {
				return 0, resetErr
			}
		}
		return 0, fmt.Errorf("list request objects: %w", err)
	}
	if len(page.Keys) > limit || (page.NextCursor != "" && page.NextCursor == cursor) {
		return 0, errors.New("invalid request object page")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	transaction, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	tx := &writeTx{Tx: transaction, conn: conn}
	defer tx.close()
	result, err := tx.ExecContext(ctx, `update request_reconciliation_cursor set continuation_token = ?, generation = generation + 1 where id = 1 and generation = ?`, page.NextCursor, generation)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	now := s.now().UTC()
	count := 0
	for _, key := range page.Keys {
		if !strings.HasPrefix(key, ObjectPrefix) || key == ObjectPrefix {
			continue
		}
		var protected bool
		// A deadline-expired generation can never accept again, even if its reaper
		// transition is delayed. Any DB error must fail closed, never mean absent.
		err := tx.QueryRowContext(ctx, `select exists (select 1 from requests where expires_at > ? and ((state = 'submitted' and final_key = ?) or (state = 'uploading' and reservation_expires_at > ? and (upload_key = ? or final_key = ?))))`, now.Format(timeFormat), key, now.Format(timeFormat), key, key).Scan(&protected)
		if err != nil {
			return 0, err
		}
		if protected {
			continue
		}
		jobID, err := events.NewJobID()
		if err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, `insert or ignore into request_reconciliation_pending (object_key, job_id) values (?, ?)`, key, jobID)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}
		if s.opts.Outbox == nil {
			return 0, ErrStorage
		}
		if _, err := s.opts.Outbox.EnqueueTx(ctx, tx.Tx, events.JobEvent{JobID: jobID, Kind: events.KindDeleteOCIObject, ObjectKey: key, Reason: events.ReasonOrphan, RequestedAt: now}); err != nil {
			return 0, err
		}
		count++
	}
	// No request deadline applies to the scan transaction itself.
	if err := s.commit(tx, time.Time{}); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) AcknowledgeObjectCleanup(ctx context.Context, jobID, key string) error {
	if strings.TrimSpace(jobID) == "" || !strings.HasPrefix(key, ObjectPrefix) || key == ObjectPrefix {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `delete from request_reconciliation_pending where object_key = ? and job_id = ?`, key, jobID)
	return err
}
