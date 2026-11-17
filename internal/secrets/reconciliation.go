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

// ReconcileOnce visits one page per configured backend, independently of the
// expiry batch. Each backend keeps its own cursor and pending claims; both
// commit with the outbox and survive API restarts.
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
	// Claim this generation before live-row reads. A concurrent scanner may have
	// advanced the same page while List was in flight; only one result can commit.
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
		// The conflict target is the (backend, object_key) primary key: a job_id
		// collision must fail loudly rather than silently skip a real orphan's
		// cleanup enqueue.
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
	// A just-opened body may still be streaming to the recipient; its secret
	// row is already gone, and the open handler enqueues its delete when the
	// stream ends. The grace record protects it until the stream deadline has
	// certainly passed, then it is reconciled like any orphan.
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
	err = tx.QueryRowContext(ctx, `select state, expires_at, created_at, consumed_at from secrets where storage_backend in ('s3_object', 'r2_object') and storage_key = $1 and reclaim_enqueued_at is null`, key).Scan(&state, &expiresAt, &createdAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check live object protection: %w", err)
	}
	// Only a locked row is still consumed here (opens delete their row), and
	// lockout queues its object delete at once; the grace stays as a margin.
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

// AcknowledgeObjectCleanup removes only the claim for this completed worker job.
// Old or duplicate acknowledgements cannot clear a newer job for a late PUT. An
// empty backend is a worker predating dual-backend support: its claims live on
// the primary.
func (s *Store) AcknowledgeObjectCleanup(ctx context.Context, backend, jobID, key string) error {
	if strings.TrimSpace(jobID) == "" || !strings.HasPrefix(key, managedObjectPrefix) || key == managedObjectPrefix {
		return ErrInvalidInput
	}
	if backend == "" {
		backend = storage.PrimaryBackend
	}
	_, err := s.db.ExecContext(ctx, `delete from object_reconciliation_pending_v2 where backend = $1 and object_key = $2 and job_id = $3`, backend, key, jobID)
	if err != nil {
		return fmt.Errorf("acknowledge object cleanup: %w", err)
	}
	return nil
}
