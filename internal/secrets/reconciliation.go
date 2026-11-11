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
	if err := r.db.QueryRowContext(ctx, `select continuation_token, generation from object_reconciliation_cursor_v2 where backend = ?`, backend).Scan(&cursor, &generation); err != nil {
		return 0, fmt.Errorf("read object reconciliation cursor: %w", err)
	}
	limit := min(r.batchSize, 1000) // ListObjectsV2 permits at most 1,000 keys per page.
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	page, err := objects.List(listCtx, managedObjectPrefix, cursor, limit)
	cancel()
	if err != nil {
		if errors.Is(err, storage.ErrInvalidCursor) && cursor != "" {
			if _, resetErr := r.db.ExecContext(ctx, `update object_reconciliation_cursor_v2 set continuation_token = '', generation = generation + 1 where backend = ? and generation = ?`, backend, generation); resetErr != nil {
				return 0, fmt.Errorf("reset object reconciliation cursor: %w", resetErr)
			}
		}
		return 0, fmt.Errorf("list managed objects: %w", err)
	}
	if len(page.Keys) > limit || (page.NextCursor != "" && page.NextCursor == cursor) {
		return 0, errors.New("invalid object listing page")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	// Claim this generation before live-row reads. A concurrent scanner may have
	// advanced the same page while List was in flight; only one result can commit.
	result, err := tx.ExecContext(ctx, `update object_reconciliation_cursor_v2 set continuation_token = ?, generation = generation + 1 where backend = ? and generation = ?`, page.NextCursor, backend, generation)
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
		result, err := tx.ExecContext(ctx, `insert or ignore into object_reconciliation_pending_v2 (backend, object_key, job_id) values (?,?,?)`, backend, key, jobID)
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
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "rollback")
		return 0, fmt.Errorf("commit object reconciliation: %w", err)
	}
	return count, nil
}

func (r *Reaper) protectObjectTx(ctx context.Context, tx *sql.Tx, key string, now time.Time) (bool, error) {
	// A just-opened body may still be streaming to the recipient; its secret
	// row is already gone, and the open handler enqueues its delete when the
	// stream ends. The grace record protects it until the stream deadline has
	// certainly passed, then it is reconciled like any orphan.
	var graceRaw string
	err := tx.QueryRowContext(ctx, `select grace_until from secret_open_grace where object_key = ?`, key).Scan(&graceRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("check streaming object protection: %w", err)
	}
	if err == nil {
		graceUntil, err := parseTime(graceRaw)
		if err != nil {
			return false, fmt.Errorf("parse streaming grace: %w", err)
		}
		if now.Before(graceUntil) {
			return true, nil
		}
	}
	var state, expiresRaw, createdRaw string
	var consumedRaw sql.NullString
	err = tx.QueryRowContext(ctx, `select state, expires_at, created_at, consumed_at from secrets where storage_backend in ('s3_object', 'r2_object') and storage_key = ? and reclaim_enqueued_at is null`, key).Scan(&state, &expiresRaw, &createdRaw, &consumedRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check live object protection: %w", err)
	}
	// Only a locked row is still consumed here (opens delete their row), and
	// lockout queues its object delete at once; the grace stays as a margin.
	if consumedRaw.Valid {
		consumed, err := parseTime(consumedRaw.String)
		if err != nil {
			return false, fmt.Errorf("parse protected open time: %w", err)
		}
		return now.Before(consumed.Add(openObjectGrace)), nil
	}
	expires, err := parseTime(expiresRaw)
	if err != nil {
		return false, fmt.Errorf("parse protected expiry: %w", err)
	}
	if !now.Before(expires) {
		return false, nil
	}
	if state == "active" {
		return true, nil
	}
	if state != "pending_upload" {
		return false, errors.New("unknown protected delivery state")
	}
	created, err := parseTime(createdRaw)
	if err != nil {
		return false, fmt.Errorf("parse protected upload time: %w", err)
	}
	return now.Before(created.Add(r.pendingTTL)), nil
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
	_, err := s.db.ExecContext(ctx, `delete from object_reconciliation_pending_v2 where backend = ? and object_key = ? and job_id = ?`, backend, key, jobID)
	if err != nil {
		return fmt.Errorf("acknowledge object cleanup: %w", err)
	}
	return nil
}
