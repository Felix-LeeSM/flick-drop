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

// ReconcileOnce visits a single page independently of the expiry batch. The
// cursor and pending claims commit with the outbox, and survive API restarts.
func (r *Reaper) ReconcileOnce(ctx context.Context) (int, error) {
	if r.store.objects == nil {
		return 0, nil
	}
	var cursor string
	var generation int64
	if err := r.db.QueryRowContext(ctx, `select continuation_token, generation from object_reconciliation_cursor where id = 1`).Scan(&cursor, &generation); err != nil {
		return 0, fmt.Errorf("read object reconciliation cursor: %w", err)
	}
	limit := min(r.batchSize, 1000) // ListObjectsV2 permits at most 1,000 keys per page.
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	page, err := r.store.objects.List(listCtx, managedObjectPrefix, cursor, limit)
	cancel()
	if err != nil {
		if errors.Is(err, storage.ErrInvalidCursor) && cursor != "" {
			if _, resetErr := r.db.ExecContext(ctx, `update object_reconciliation_cursor set continuation_token = '', generation = generation + 1 where id = 1 and generation = ?`, generation); resetErr != nil {
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
	result, err := tx.ExecContext(ctx, `update object_reconciliation_cursor set continuation_token = ?, generation = generation + 1 where id = 1 and generation = ?`, page.NextCursor, generation)
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
		result, err := tx.ExecContext(ctx, `insert or ignore into object_reconciliation_pending (object_key, job_id) values (?,?)`, key, jobID)
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
		if _, err := r.outbox.EnqueueTx(ctx, tx, events.JobEvent{JobID: jobID, Kind: events.KindDeleteOCIObject, ObjectKey: key, Reason: events.ReasonOrphan, RequestedAt: now}); err != nil {
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
	var state, expiresRaw, createdRaw string
	err := tx.QueryRowContext(ctx, `select state, expires_at, created_at from secrets where storage_backend = 's3_object' and storage_key = ? and consumed_at is null and reclaim_enqueued_at is null`, key).Scan(&state, &expiresRaw, &createdRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check live object protection: %w", err)
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
// Old or duplicate acknowledgements cannot clear a newer job for a late PUT.
func (s *Store) AcknowledgeObjectCleanup(ctx context.Context, jobID, key string) error {
	if strings.TrimSpace(jobID) == "" || !strings.HasPrefix(key, managedObjectPrefix) || key == managedObjectPrefix {
		return ErrInvalidInput
	}
	_, err := s.db.ExecContext(ctx, `delete from object_reconciliation_pending where object_key = ? and job_id = ?`, key, jobID)
	if err != nil {
		return fmt.Errorf("acknowledge object cleanup: %w", err)
	}
	return nil
}
