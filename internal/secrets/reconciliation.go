package secrets

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

// Each backend keeps its own cursor and pending claims; both commit with the outbox and survive restarts.
func (r *Reaper) ReconcileOnce(ctx context.Context) (int, error) {
	if r.store.objects == nil {
		return 0, nil
	}
	total := 0
	err := r.store.objects.Each(func(backend string, objects storage.ObjectStore) error {
		count, err := r.reconcileBackend(ctx, objects, backend)
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

func (r *Reaper) reconcileBackend(ctx context.Context, objects storage.ObjectStore, backend string) (int, error) {
	var cursor string
	var generation int64
	if _, err := r.db.ExecContext(ctx, `insert into object_reconciliation_cursor_v2 (backend) values ($1) on conflict do nothing`, backend); err != nil {
		return 0, fmt.Errorf("seed object reconciliation cursor: %w", err)
	}
	if err := r.db.QueryRowContext(ctx, `select continuation_token, generation from object_reconciliation_cursor_v2 where backend = $1`, backend).Scan(&cursor, &generation); err != nil {
		return 0, fmt.Errorf("read object reconciliation cursor: %w", err)
	}
	limit := min(r.batchSize, 1000) // ListObjectsV2 permits at most 1,000 keys per page.
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	page, err := objects.List(listCtx, managedObjectPrefix, cursor, limit)
	cancel()
	if err != nil {
		if errors.Is(err, storage.ErrInvalidCursor) && cursor != "" {
			if _, resetErr := r.db.ExecContext(ctx, `update object_reconciliation_cursor_v2 set continuation_token = '', generation = generation + 1 where backend = $1 and generation = $2`, backend, generation); resetErr != nil {
				return 0, fmt.Errorf("reset object reconciliation cursor: %w", resetErr)
			}
		}
		return 0, fmt.Errorf("list managed objects: %w", err)
	}
	if len(page.Keys) > limit || (page.NextCursor != "" && page.NextCursor == cursor) {
		return 0, errors.New("invalid object listing page")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	// Claim this generation before live-row reads; only one concurrent scanner's page can commit.
	result, err := tx.ExecContext(ctx, `update object_reconciliation_cursor_v2 set continuation_token = $1, generation = generation + 1 where backend = $2 and generation = $3`, page.NextCursor, backend, generation)
	if err != nil {
		return 0, fmt.Errorf("advance object reconciliation cursor: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	count := 0
	now := r.now().UTC()
	for _, key := range page.Keys {
		if !strings.HasPrefix(key, managedObjectPrefix) || key == managedObjectPrefix {
			continue
		}
		live, err := r.protectObjectTx(ctx, tx, key, now)
		if err != nil {
			return 0, err
		}
		if live {
			continue
		}
		jobID, err := events.NewJobID()
		if err != nil {
			return 0, err
		}
		// A job_id collision must fail loudly rather than silently skip a real orphan's cleanup.
		result, err := tx.ExecContext(ctx, `insert into object_reconciliation_pending_v2 (backend, object_key, job_id) values ($1, $2, $3)
			on conflict (backend, object_key) do nothing`, backend, key, jobID)
		if err != nil {
			return 0, fmt.Errorf("claim object cleanup: %w", err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}
		if _, err := r.outbox.EnqueueTx(ctx, tx, events.JobEvent{JobID: jobID, Kind: events.KindDeleteOCIObject, Backend: backend, ObjectKey: key, Reason: events.ReasonOrphan, RequestedAt: now}); err != nil {
			return 0, fmt.Errorf("enqueue reconciled object: %w", err)
		}
		count++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit object reconciliation: %w", err)
	}
	return count, nil
}

func (r *Reaper) protectObjectTx(ctx context.Context, tx *sql.Tx, key string, now time.Time) (bool, error) {
	// The secret row is gone after open; the grace record protects a still-streaming body until the deadline.
	var graceUntil time.Time
	err := tx.QueryRowContext(ctx, `select grace_until from secret_open_grace where object_key = $1`, key).Scan(&graceUntil)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("check streaming object protection: %w", err)
	}
	if err == nil && now.Before(graceUntil) {
		return true, nil
	}
	var state string
	var expiresAt, createdAt time.Time
	var consumedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `select state, expires_at, created_at, consumed_at from secrets where storage_backend <> 'sqlite_blob' and storage_key = $1 and reclaim_enqueued_at is null`, key).Scan(&state, &expiresAt, &createdAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check live object protection: %w", err)
	}
	// A consumed row is protected exactly until the grace ends, never longer.
	if consumedAt.Valid {
		return now.Before(consumedAt.Time.Add(openObjectGrace)), nil
	}
	if !now.Before(expiresAt) {
		return false, nil
	}
	if state == "active" {
		return true, nil
	}
	if state != "pending_upload" {
		return false, errors.New("unknown protected delivery state")
	}
	return now.Before(createdAt.Add(r.pendingTTL)), nil
}

// Old or duplicate acknowledgements cannot clear a newer job for a late PUT.
func (s *Store) AcknowledgeObjectCleanup(ctx context.Context, backend, jobID, key string) error {
	if strings.TrimSpace(jobID) == "" || backend == "" || !strings.HasPrefix(key, managedObjectPrefix) || key == managedObjectPrefix {
		return ErrInvalidInput
	}
	_, err := s.db.ExecContext(ctx, `delete from object_reconciliation_pending_v2 where backend = $1 and object_key = $2 and job_id = $3`, backend, key, jobID)
	if err != nil {
		return fmt.Errorf("acknowledge object cleanup: %w", err)
	}
	return nil
}
