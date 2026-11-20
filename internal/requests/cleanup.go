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

// openObjectGrace must outlast OpenStreamTimeout. Same values as internal/secrets/age.go.
const (
	OpenStreamTimeout = 30 * time.Minute
	openObjectGrace   = OpenStreamTimeout + 5*time.Minute
)

func enqueueObjectTx(ctx context.Context, tx *sql.Tx, outbox OutboxEnqueuer, backend, key, reason string, now time.Time) error {
	if !strings.HasPrefix(key, ObjectPrefix) || key == ObjectPrefix || outbox == nil {
		return ErrStorage
	}
	jobID, err := events.NewJobID()
	if err != nil {
		return err
	}
	_, err = outbox.EnqueueTx(ctx, tx, events.JobEvent{JobID: jobID, Kind: events.KindDeleteOCIObject, Backend: backend, ObjectKey: key, Reason: reason, RequestedAt: now})
	return err
}

func enqueueRequestKeysTx(ctx context.Context, tx *sql.Tx, outbox OutboxEnqueuer, id, reason string, now time.Time) error {
	var backend string
	var upload, final sql.NullString
	if err := tx.QueryRowContext(ctx, `select storage_backend, upload_key, final_key from requests where id = $1`, id).Scan(&backend, &upload, &final); err != nil {
		return err
	}
	for _, key := range []sql.NullString{upload, final} {
		if key.Valid {
			if err := enqueueObjectTx(ctx, tx, outbox, backend, key.String, reason, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// Fences the former generation permanently before its cleanup can commit; exhaustion never wraps.
func endReservationTx(ctx context.Context, tx *sql.Tx, outbox OutboxEnqueuer, id string, generation int, now time.Time) error {
	if err := enqueueRequestKeysTx(ctx, tx, outbox, id, events.ReasonOrphan, now); err != nil {
		return err
	}
	next, state := generation+1, "waiting"
	if generation == MaxGeneration {
		next, state = generation, "unavailable"
	}
	changed, err := tx.ExecContext(ctx, `update requests set state = $1, generation = $2, storage_backend = 'sqlite_blob', attempt_token_hash = null, attempt_body_hash = null, kind = null, size_bytes = null, envelope_json = null, upload_key = null, final_key = null, ciphertext_sha256 = null, reservation_expires_at = null, encrypted_note = case when $3 = 'unavailable' then null else encrypted_note end where id = $4 and state = 'uploading' and generation = $5`, state, next, state, id, generation)
	if err != nil {
		return err
	}
	return requireTransition(changed)
}

// FOR UPDATE SKIP LOCKED lets API replicas reap disjoint rows; outbox failure rolls back the deletion.
func PurgeExpiredTx(ctx context.Context, tx *sql.Tx, now time.Time, limit int, outboxes ...OutboxEnqueuer) error {
	var outbox OutboxEnqueuer
	if len(outboxes) != 0 {
		outbox = outboxes[0]
	}
	rows, err := tx.QueryContext(ctx, `select id, generation from requests
		where state = 'uploading' and reservation_expires_at <= $1 and expires_at > $2
		order by reservation_expires_at, id limit $3
		for update skip locked`, now.UTC(), now.UTC(), limit)
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
	// A possibly streaming object keeps its row, and its final_key protection, until the open grace passes.
	rows, err = tx.QueryContext(ctx, `select id from requests
		where expires_at <= $1 and not (state = 'consumed' and final_key is not null and consumed_at > $2)
		order by expires_at, id limit $3
		for update skip locked`, now.UTC(), now.UTC().Add(-openObjectGrace), limit)
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
		if _, err := tx.ExecContext(ctx, `delete from requests where id = $1`, id); err != nil {
			return err
		}
	}
	return nil
}

// Owns only managed/requests/; cursors are per backend and independent of managed/secrets/.
func (s *Store) ReconcileOnce(ctx context.Context, limit int) (int, error) {
	if s.opts.Objects == nil {
		return 0, nil
	}
	if limit < 1 {
		return 0, errors.New("invalid request reconciliation batch")
	}
	limit = min(limit, 1000)
	total := 0
	err := s.opts.Objects.Each(func(backend string, objects storage.ObjectStore) error {
		count, err := s.reconcileBackend(ctx, objects, backend, limit)
		if err != nil {
			return err
		}
		total += count
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func (s *Store) reconcileBackend(ctx context.Context, objects storage.ObjectStore, backend string, limit int) (int, error) {
	var cursor string
	var generation int64
	if _, err := s.db.ExecContext(ctx, `insert into request_reconciliation_cursor_v2 (backend) values ($1) on conflict do nothing`, backend); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `select continuation_token, generation from request_reconciliation_cursor_v2 where backend = $1`, backend).Scan(&cursor, &generation); err != nil {
		return 0, err
	}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	page, err := objects.List(listCtx, ObjectPrefix, cursor, limit)
	cancel()
	if err != nil {
		if errors.Is(err, storage.ErrInvalidCursor) && cursor != "" {
			if _, resetErr := s.db.ExecContext(ctx, `update request_reconciliation_cursor_v2 set continuation_token = '', generation = generation + 1 where backend = $1 and generation = $2`, backend, generation); resetErr != nil {
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
	// The generation-fenced cursor update is this scan's claim: a losing replica updates zero rows and enqueues nothing.
	result, err := tx.ExecContext(ctx, `update request_reconciliation_cursor_v2 set continuation_token = $1, generation = generation + 1 where backend = $2 and generation = $3`, page.NextCursor, backend, generation)
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
		// Any DB error must fail closed, never mean absent. A just-opened object is protected for the stream grace.
		err := tx.QueryRowContext(ctx, `select exists (select 1 from requests
			where (state = 'consumed' and final_key = $1 and consumed_at > $2)
				or (expires_at > $3 and ((state = 'submitted' and final_key = $1)
					or (state = 'uploading' and reservation_expires_at > $3 and (upload_key = $1 or final_key = $1)))))`,
			key, now.Add(-openObjectGrace), now).Scan(&protected)
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
		result, err := tx.ExecContext(ctx, `insert into request_reconciliation_pending_v2 (backend, object_key, job_id) values ($1, $2, $3) on conflict do nothing`, backend, key, jobID)
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
		if _, err := s.opts.Outbox.EnqueueTx(ctx, tx.Tx, events.JobEvent{JobID: jobID, Kind: events.KindDeleteOCIObject, Backend: backend, ObjectKey: key, Reason: events.ReasonOrphan, RequestedAt: now}); err != nil {
			return 0, err
		}
		count++
	}
	if err := s.commit(tx, time.Time{}); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) AcknowledgeObjectCleanup(ctx context.Context, backend, jobID, key string) error {
	if strings.TrimSpace(jobID) == "" || backend == "" || !strings.HasPrefix(key, ObjectPrefix) || key == ObjectPrefix {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `delete from request_reconciliation_pending_v2 where backend = $1 and object_key = $2 and job_id = $3`, backend, key, jobID)
	return err
}
