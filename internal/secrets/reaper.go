package secrets

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
)

const (
	DefaultReaperInterval  = 60 * time.Second
	DefaultReaperBatchSize = 50
)

// claimReclaimableSQL atomically claims reclaimable secrets and returns the
// columns needed to build their cleanup events in a single statement. The
// WHERE guard (reclaim_enqueued_at IS NULL) makes the claim multi-instance and
// multi-tick safe: only one claimer can flip the timestamp per row. Consumed
// secrets are excluded because /open already enqueued their cleanup. Reason is
// derived from state: active → expired, pending_upload → orphan.
const claimReclaimableSQL = `with candidates as (
	select id, state, storage_backend, storage_key
	from secrets
	where reclaim_enqueued_at is null
		and consumed_at is null
		and (
			(state = 'active' and expires_at < ?)
			or (state = 'pending_upload' and created_at < ?)
		)
	order by expires_at, created_at
	limit ?
)
update secrets
set reclaim_enqueued_at = ?, updated_at = ?
from candidates
where secrets.id = candidates.id
returning id, state, storage_backend, storage_key`

// outboxEnqueuer is the subset of *events.OutboxStore the reaper needs, kept as
// an interface so tests can inject a failing fake for atomicity checks.
type outboxEnqueuer interface {
	EnqueueTx(context.Context, *sql.Tx, events.JobEvent) (events.OutboxRecord, error)
}

type Reaper struct {
	db         *sql.DB
	store      *Store
	outbox     outboxEnqueuer
	now        func() time.Time
	batchSize  int
	pendingTTL time.Duration
}

type ReaperOptions struct {
	BatchSize int
}

type ReaperLoopOptions struct {
	Interval time.Duration
	Logf     func(string, ...any)
}

// claimedRow mirrors the columns returned by claimReclaimableSQL.
type claimedRow struct {
	id             string
	state          string
	storageBackend string
	storageKey     string
}

func NewReaper(db *sql.DB, store *Store, outbox outboxEnqueuer, opts ReaperOptions) (*Reaper, error) {
	if db == nil {
		return nil, fmt.Errorf("db is required")
	}
	if store == nil {
		return nil, fmt.Errorf("secret store is required")
	}
	if outbox == nil {
		return nil, fmt.Errorf("outbox is required")
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultReaperBatchSize
	}
	return &Reaper{
		db:         db,
		store:      store,
		outbox:     outbox,
		now:        func() time.Time { return time.Now().UTC() },
		batchSize:  batchSize,
		pendingTTL: store.pendingTTL,
	}, nil
}

func (r *Reaper) SetNowForTest(now func() time.Time) {
	r.now = now
}

// ClaimOnce claims one batch of reclaimable secrets, hard-deletes their rows,
// and enqueues an object-delete event for any S3-backed row — all in one
// transaction. A failure rolls the claim back, leaving reclaim_enqueued_at NULL
// so the next tick retries. Returns the number of secrets reaped.
func (r *Reaper) ClaimOnce(ctx context.Context) (int, error) {
	now := r.now().UTC()
	orphanCutoff := now.Add(-r.pendingTTL)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin reaper claim tx: %w", err)
	}
	defer rollback(tx)

	rows, err := tx.QueryContext(ctx, claimReclaimableSQL,
		formatTime(now), formatTime(orphanCutoff), r.batchSize,
		formatTime(now), formatTime(now),
	)
	if err != nil {
		return 0, fmt.Errorf("claim reclaimable secrets: %w", err)
	}

	// Collect before mutating: reusing the tx while a cursor is open can conflict
	// on SQLite, so drain and close the rows first.
	var batch []claimedRow
	for rows.Next() {
		var c claimedRow
		if err := rows.Scan(&c.id, &c.state, &c.storageBackend, &c.storageKey); err != nil {
			return 0, fmt.Errorf("scan claimed secret: %w", err)
		}
		batch = append(batch, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("iterate claimed secrets: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close claimed rows: %w", err)
	}

	for _, c := range batch {
		if err := r.reclaimRow(ctx, tx, c, now); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit reaper claim tx: %w", err)
	}
	return len(batch), nil
}

func (r *Reaper) reclaimRow(ctx context.Context, tx *sql.Tx, c claimedRow, now time.Time) error {
	if err := r.store.ReclaimTx(ctx, tx, c.id); err != nil {
		return err
	}
	if c.storageBackend != StorageS3 {
		return nil
	}
	jobID, err := events.NewJobID()
	if err != nil {
		return fmt.Errorf("generate object delete job id for %s: %w", c.id, err)
	}
	reason := events.ReasonExpired
	if c.state == "pending_upload" {
		reason = events.ReasonOrphan
	}
	if _, err := r.outbox.EnqueueTx(ctx, tx, events.JobEvent{
		JobID:       jobID,
		Kind:        events.KindDeleteOCIObject,
		ObjectKey:   c.storageKey,
		Reason:      reason,
		RequestedAt: now,
	}); err != nil {
		return fmt.Errorf("enqueue object delete for %s: %w", c.id, err)
	}
	return nil
}

// claimRunner is the subset of *Reaper the loop drives, kept as an interface so
// tests can inject a fake (mirrors events.duePublisher).
type claimRunner interface {
	ClaimOnce(context.Context) (int, error)
}

// RunReaper ticks ClaimOnce at a fixed interval until ctx is cancelled. It is a
// structural mirror of events.RunOutboxPublisher.
func RunReaper(ctx context.Context, reaper claimRunner, opts ReaperLoopOptions) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if reaper == nil {
		return fmt.Errorf("reaper is required")
	}

	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultReaperInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		runReaperTick(ctx, reaper, opts.Logf)

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func runReaperTick(ctx context.Context, reaper claimRunner, logf func(string, ...any)) {
	claimed, err := reaper.ClaimOnce(ctx)
	if err != nil {
		if logf != nil {
			logf("reaper error: %v", err)
		}
		return
	}
	if logf != nil && claimed > 0 {
		logf("reaper batch: reclaimed=%d", claimed)
	}
}
