package secrets

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
)

func newTestOutbox(t *testing.T, conn *sql.DB) *events.OutboxStore {
	t.Helper()
	store, err := events.NewOutboxStore(conn, "test.jobs")
	if err != nil {
		t.Fatalf("new outbox store: %v", err)
	}
	return store
}

func newTestReaper(t *testing.T, conn *sql.DB, store *Store, outbox outboxEnqueuer, batchSize int) *Reaper {
	t.Helper()
	reaper, err := NewReaper(conn, store, outbox, ReaperOptions{BatchSize: batchSize})
	if err != nil {
		t.Fatalf("new reaper: %v", err)
	}
	return reaper
}

type secretFixture struct {
	id             string
	kind           string
	storageBackend string
	storageKey     string
	nonce          string
	state          string
	expiresAt      time.Time
	consumedAt     time.Time // zero → NULL
	createdAt      time.Time
	updatedAt      time.Time
	sizeBytes      int64
}

func insertSecret(t *testing.T, ctx context.Context, conn *sql.DB, f secretFixture) {
	t.Helper()
	if f.id == "" {
		f.id = "sec_1"
	}
	if f.kind == "" {
		f.kind = "text"
	}
	if f.storageBackend == "" {
		f.storageBackend = "sqlite_blob"
	}
	if f.storageKey == "" {
		f.storageKey = f.id
	}
	if f.nonce == "" {
		f.nonce = "nonce"
	}
	if f.state == "" {
		f.state = "active"
	}
	if f.sizeBytes == 0 {
		f.sizeBytes = 1
	}

	var consumed any
	if !f.consumedAt.IsZero() {
		consumed = formatTime(f.consumedAt)
	}
	_, err := conn.ExecContext(ctx, `insert into secrets (
		id, kind, storage_backend, storage_key, nonce, size_bytes,
		max_views, view_count, failed_access_count, state, expires_at, consumed_at, created_at, updated_at
	) values (?,?,?,?,?,?,1,0,0,?,?,?,?,?)`,
		f.id, f.kind, f.storageBackend, f.storageKey, f.nonce, f.sizeBytes,
		f.state, formatTime(f.expiresAt), consumed, formatTime(f.createdAt), formatTime(f.updatedAt),
	)
	if err != nil {
		t.Fatalf("insert secret %s: %v", f.id, err)
	}
}

func countSecrets(t *testing.T, ctx context.Context, conn *sql.DB) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(ctx, `select count(*) from secrets`).Scan(&n); err != nil {
		t.Fatalf("count secrets: %v", err)
	}
	return n
}

func reclaimEnqueuedAt(t *testing.T, ctx context.Context, conn *sql.DB, id string) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := conn.QueryRowContext(ctx, `select reclaim_enqueued_at from secrets where id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("read reclaim_enqueued_at for %s: %v", id, err)
	}
	return v
}

func readOutboxEvents(t *testing.T, ctx context.Context, conn *sql.DB) []events.JobEvent {
	t.Helper()
	rows, err := conn.QueryContext(ctx, `select payload_json from outbox_events order by created_at, id`)
	if err != nil {
		t.Fatalf("query outbox events: %v", err)
	}
	defer rows.Close()

	var out []events.JobEvent
	for rows.Next() {
		var payloadJSON string
		if err := rows.Scan(&payloadJSON); err != nil {
			t.Fatalf("scan outbox payload: %v", err)
		}
		event, err := events.DecodeJobEvent([]byte(payloadJSON))
		if err != nil {
			t.Fatalf("decode outbox event: %v", err)
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate outbox events: %v", err)
	}
	return out
}

func TestReaperClaimsExpiredActive(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	outbox := newTestOutbox(t, conn)
	reaper := newTestReaper(t, conn, store, outbox, 0)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	reaper.SetNowForTest(func() time.Time { return now })

	insertSecret(t, ctx, conn, secretFixture{
		id:        "sec_expired",
		expiresAt: now.Add(-1 * time.Minute),
		createdAt: now.Add(-10 * time.Minute),
		updatedAt: now.Add(-10 * time.Minute),
	})

	claimed, err := reaper.ClaimOnce(ctx)
	if err != nil {
		t.Fatalf("claim once: %v", err)
	}
	if claimed != 1 {
		t.Fatalf("claimed = %d, want 1", claimed)
	}
	if got := countSecrets(t, ctx, conn); got != 0 {
		t.Fatalf("secrets after reap = %d, want 0 (row hard-deleted)", got)
	}
	// sqlite_blob row enqueues no object-delete event.
	if events := readOutboxEvents(t, ctx, conn); len(events) != 0 {
		t.Fatalf("outbox events = %d, want 0 for inline secret", len(events))
	}
}

func TestReaperClaimsOrphanPending(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	outbox := newTestOutbox(t, conn)
	reaper := newTestReaper(t, conn, store, outbox, 0)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	reaper.SetNowForTest(func() time.Time { return now })

	// pending_upload older than PendingTTL (default 15m) is an orphan.
	insertSecret(t, ctx, conn, secretFixture{
		id:        "sec_orphan",
		state:     "pending_upload",
		createdAt: now.Add(-20 * time.Minute),
		updatedAt: now.Add(-20 * time.Minute),
	})

	claimed, err := reaper.ClaimOnce(ctx)
	if err != nil {
		t.Fatalf("claim once: %v", err)
	}
	if claimed != 1 {
		t.Fatalf("claimed = %d, want 1", claimed)
	}
	if got := countSecrets(t, ctx, conn); got != 0 {
		t.Fatalf("secrets after reap = %d, want 0", got)
	}
}

func TestReaperEnqueuesObjectDeleteForS3(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	outbox := newTestOutbox(t, conn)
	reaper := newTestReaper(t, conn, store, outbox, 0)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	reaper.SetNowForTest(func() time.Time { return now })

	insertSecret(t, ctx, conn, secretFixture{
		id:             "sec_s3",
		storageBackend: "s3_object",
		storageKey:     "obj_key_s3",
		expiresAt:      now.Add(-1 * time.Minute),
		createdAt:      now.Add(-10 * time.Minute),
		updatedAt:      now.Add(-10 * time.Minute),
	})

	if _, err := reaper.ClaimOnce(ctx); err != nil {
		t.Fatalf("claim once: %v", err)
	}

	got := readOutboxEvents(t, ctx, conn)
	if len(got) != 1 {
		t.Fatalf("outbox events = %d, want 1", len(got))
	}
	if got[0].Kind != events.KindDeleteOCIObject {
		t.Fatalf("kind = %q, want %q", got[0].Kind, events.KindDeleteOCIObject)
	}
	if got[0].ObjectKey != "obj_key_s3" {
		t.Fatalf("object_key = %q, want obj_key_s3", got[0].ObjectKey)
	}
	if got[0].Reason != events.ReasonExpired {
		t.Fatalf("reason = %q, want %q", got[0].Reason, events.ReasonExpired)
	}
}

func TestReaperOrphanEnqueuesOrphanReason(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	outbox := newTestOutbox(t, conn)
	reaper := newTestReaper(t, conn, store, outbox, 0)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	reaper.SetNowForTest(func() time.Time { return now })

	insertSecret(t, ctx, conn, secretFixture{
		id:             "sec_orphan_s3",
		state:          "pending_upload",
		storageBackend: "s3_object",
		storageKey:     "obj_key_orphan",
		createdAt:      now.Add(-20 * time.Minute),
		updatedAt:      now.Add(-20 * time.Minute),
	})

	if _, err := reaper.ClaimOnce(ctx); err != nil {
		t.Fatalf("claim once: %v", err)
	}

	got := readOutboxEvents(t, ctx, conn)
	if len(got) != 1 || got[0].Reason != events.ReasonOrphan {
		t.Fatalf("outbox = %+v, want 1 event with reason %q", got, events.ReasonOrphan)
	}
}

func TestReaperSkipsConsumed(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	outbox := newTestOutbox(t, conn)
	reaper := newTestReaper(t, conn, store, outbox, 0)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	reaper.SetNowForTest(func() time.Time { return now })

	// Expired AND consumed: /open already enqueued its cleanup.
	insertSecret(t, ctx, conn, secretFixture{
		id:         "sec_consumed",
		expiresAt:  now.Add(-1 * time.Minute),
		consumedAt: now.Add(-2 * time.Minute),
		createdAt:  now.Add(-10 * time.Minute),
		updatedAt:  now.Add(-2 * time.Minute),
	})

	claimed, err := reaper.ClaimOnce(ctx)
	if err != nil {
		t.Fatalf("claim once: %v", err)
	}
	if claimed != 0 {
		t.Fatalf("claimed = %d, want 0 (consumed secrets are skipped)", claimed)
	}
	if got := countSecrets(t, ctx, conn); got != 1 {
		t.Fatalf("secrets = %d, want 1 (consumed row preserved)", got)
	}
}

func TestReaperSkipsUnexpired(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	outbox := newTestOutbox(t, conn)
	reaper := newTestReaper(t, conn, store, outbox, 0)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	reaper.SetNowForTest(func() time.Time { return now })

	insertSecret(t, ctx, conn, secretFixture{ // active, not expired
		id:        "sec_live",
		expiresAt: now.Add(1 * time.Hour),
		createdAt: now.Add(-1 * time.Minute),
		updatedAt: now.Add(-1 * time.Minute),
	})
	insertSecret(t, ctx, conn, secretFixture{ // pending_upload within PendingTTL
		id:        "sec_pending_live",
		state:     "pending_upload",
		createdAt: now.Add(-5 * time.Minute),
		updatedAt: now.Add(-5 * time.Minute),
	})

	claimed, err := reaper.ClaimOnce(ctx)
	if err != nil {
		t.Fatalf("claim once: %v", err)
	}
	if claimed != 0 {
		t.Fatalf("claimed = %d, want 0 (nothing reclaimable)", claimed)
	}
	if got := countSecrets(t, ctx, conn); got != 2 {
		t.Fatalf("secrets = %d, want 2", got)
	}
}

func TestReaperDedupAcrossTicks(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	outbox := newTestOutbox(t, conn)
	reaper := newTestReaper(t, conn, store, outbox, 0)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	reaper.SetNowForTest(func() time.Time { return now })

	insertSecret(t, ctx, conn, secretFixture{
		id:             "sec_dedup",
		storageBackend: "s3_object",
		storageKey:     "obj_dedup",
		expiresAt:      now.Add(-1 * time.Minute),
		createdAt:      now.Add(-10 * time.Minute),
		updatedAt:      now.Add(-10 * time.Minute),
	})

	if _, err := reaper.ClaimOnce(ctx); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	second, err := reaper.ClaimOnce(ctx)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second != 0 {
		t.Fatalf("second claim = %d, want 0 (already reclaimed)", second)
	}
	if got := readOutboxEvents(t, ctx, conn); len(got) != 1 {
		t.Fatalf("outbox events = %d, want 1 (enqueued exactly once)", len(got))
	}
	if countSecrets(t, ctx, conn) != 0 {
		t.Fatalf("secrets = %d, want 0", countSecrets(t, ctx, conn))
	}
}

func TestReaperBatchLimit(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	outbox := newTestOutbox(t, conn)
	reaper := newTestReaper(t, conn, store, outbox, 3)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	reaper.SetNowForTest(func() time.Time { return now })

	for i := 0; i < 10; i++ {
		insertSecret(t, ctx, conn, secretFixture{
			id:        "sec_" + string(rune('a'+i)),
			expiresAt: now.Add(-time.Duration(i+1) * time.Minute),
			createdAt: now.Add(-1 * time.Hour),
			updatedAt: now.Add(-1 * time.Hour),
		})
	}

	claimed, err := reaper.ClaimOnce(ctx)
	if err != nil {
		t.Fatalf("claim once: %v", err)
	}
	if claimed != 3 {
		t.Fatalf("claimed = %d, want 3 (batch limit)", claimed)
	}
	if got := countSecrets(t, ctx, conn); got != 7 {
		t.Fatalf("secrets = %d, want 7 remaining", got)
	}
}

func TestReaperAtomicityOnFailure(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	insertSecret(t, ctx, conn, secretFixture{
		id:             "sec_fail",
		storageBackend: "s3_object",
		storageKey:     "obj_fail",
		expiresAt:      now.Add(-1 * time.Minute),
		createdAt:      now.Add(-10 * time.Minute),
		updatedAt:      now.Add(-10 * time.Minute),
	})

	wantErr := errors.New("outbox unavailable")
	reaper := newTestReaper(t, conn, store, &fakeOutbox{err: wantErr}, 0)
	reaper.SetNowForTest(func() time.Time { return now })

	_, err := reaper.ClaimOnce(ctx)
	if !errors.Is(err, wantErr) {
		t.Fatalf("claim error = %v, want %v", err, wantErr)
	}
	// Rollback must leave the row reclaimable again (claim not persisted).
	if got := countSecrets(t, ctx, conn); got != 1 {
		t.Fatalf("secrets = %d, want 1 (row survived rollback)", got)
	}
	if v := reclaimEnqueuedAt(t, ctx, conn, "sec_fail"); v.Valid {
		t.Fatalf("reclaim_enqueued_at = %q, want NULL (claim rolled back)", v.String)
	}
}

type fakeOutbox struct {
	err      error
	enqueued []events.JobEvent
}

func (f *fakeOutbox) EnqueueTx(_ context.Context, _ *sql.Tx, e events.JobEvent) (events.OutboxRecord, error) {
	if f.err != nil {
		return events.OutboxRecord{}, f.err
	}
	f.enqueued = append(f.enqueued, e)
	return events.OutboxRecord{ID: e.JobID}, nil
}

func TestRunReaperTicksUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := &fakeClaimRunner{
		results: []int{3, 1, 0},
		cancel:  cancel,
	}
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, format)
	}

	if err := RunReaper(ctx, runner, ReaperLoopOptions{Interval: time.Millisecond, Logf: logf}); err != nil {
		t.Fatalf("run reaper: %v", err)
	}
	if runner.calls != len(runner.results) {
		t.Fatalf("ticks = %d, want %d", runner.calls, len(runner.results))
	}
	// Claimed>0 ticks log; the final 0-claim tick does not.
	if len(logs) != 2 {
		t.Fatalf("claimed-batch logs = %d, want 2", len(logs))
	}
}

func TestRunReaperContinuesAfterTickError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := &fakeClaimRunner{
		errors:  []error{errors.New("transient")},
		results: []int{0},
		cancel:  cancel,
	}
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, format)
	}

	if err := RunReaper(ctx, runner, ReaperLoopOptions{Interval: time.Millisecond, Logf: logf}); err != nil {
		t.Fatalf("run reaper: %v", err)
	}
	if runner.calls != 2 {
		t.Fatalf("ticks = %d, want 2 (loop survives error)", runner.calls)
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "reaper error") {
		t.Fatalf("logs = %v, want an error log first", logs)
	}
}

type fakeClaimRunner struct {
	calls   int
	results []int
	errors  []error // per-call; len(errors) <= len(results)
	cancel  context.CancelFunc
	mu      sync.Mutex
}

func (f *fakeClaimRunner) ClaimOnce(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	idx := f.calls
	f.calls++
	// An error tick must not cancel the loop — it logs and continues.
	if idx < len(f.errors) && f.errors[idx] != nil {
		return 0, f.errors[idx]
	}
	if idx >= len(f.results) {
		if f.cancel != nil {
			f.cancel()
		}
		return 0, nil
	}
	claimed := f.results[idx]
	// Cancel after the last planned result is consumed.
	if idx+1 >= len(f.results) && f.cancel != nil {
		f.cancel()
	}
	return claimed, nil
}

func remainingIDs(t *testing.T, ctx context.Context, conn *sql.DB) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, `select id from secrets order by id`)
	if err != nil {
		t.Fatalf("query remaining secrets: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan remaining secret: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate remaining secrets: %v", err)
	}
	return ids
}

// TestReaperOrphanCompetesWithActiveBacklog guards the unified reclaimable-since
// ordering: with a small batch, orphans that became reclaimable earlier must be
// reaped before newer expired-active rows. Under the old order-by-expires_at the
// orphans' future expires_at would always sort them behind an active backlog.
func TestReaperOrphanCompetesWithActiveBacklog(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	outbox := newTestOutbox(t, conn)
	reaper := newTestReaper(t, conn, store, outbox, 2)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	reaper.SetNowForTest(func() time.Time { return now })

	// Two orphans created 30m ago — reclaimable since now-15m (PendingTTL default).
	// Their expires_at is in the future, which the old ordering sorted last.
	for _, id := range []string{"sec_orphan_a", "sec_orphan_b"} {
		insertSecret(t, ctx, conn, secretFixture{
			id:        id,
			state:     "pending_upload",
			expiresAt: now.Add(1 * time.Hour),
			createdAt: now.Add(-30 * time.Minute),
			updatedAt: now.Add(-30 * time.Minute),
		})
	}
	// Two expired-active rows — reclaimable since now-1m (newer than the orphans).
	for _, id := range []string{"sec_active_a", "sec_active_b"} {
		insertSecret(t, ctx, conn, secretFixture{
			id:        id,
			expiresAt: now.Add(-1 * time.Minute),
			createdAt: now.Add(-10 * time.Minute),
			updatedAt: now.Add(-10 * time.Minute),
		})
	}

	first, err := reaper.ClaimOnce(ctx)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if first != 2 {
		t.Fatalf("first claim = %d, want 2", first)
	}
	if got, want := remainingIDs(t, ctx, conn), []string{"sec_active_a", "sec_active_b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after first tick = %v, want %v (orphans reaped first)", got, want)
	}

	second, err := reaper.ClaimOnce(ctx)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second != 2 {
		t.Fatalf("second claim = %d, want 2", second)
	}
	if got := countSecrets(t, ctx, conn); got != 0 {
		t.Fatalf("secrets after second tick = %d, want 0", got)
	}
}

// TestReaperPrefersOlderReclaimableActiveWhenNewerOrphan confirms the ordering is
// by reclaimable-since in both directions: an expired-active row that has been
// reclaimable longer than an orphan wins the batch slot, so neither class is
// unconditionally prioritized.
func TestReaperPrefersOlderReclaimableActiveWhenNewerOrphan(t *testing.T) {
	ctx := context.Background()
	conn := openTestDB(t, ctx)
	store := newTestStore(t, conn)
	outbox := newTestOutbox(t, conn)
	reaper := newTestReaper(t, conn, store, outbox, 1)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	reaper.SetNowForTest(func() time.Time { return now })

	// Active expired 2h ago — reclaimable since now-2h.
	insertSecret(t, ctx, conn, secretFixture{
		id:        "sec_old_active",
		expiresAt: now.Add(-2 * time.Hour),
		createdAt: now.Add(-3 * time.Hour),
		updatedAt: now.Add(-3 * time.Hour),
	})
	// Orphan created 20m ago — reclaimable since now-5m (newer).
	insertSecret(t, ctx, conn, secretFixture{
		id:        "sec_young_orphan",
		state:     "pending_upload",
		expiresAt: now.Add(1 * time.Hour),
		createdAt: now.Add(-20 * time.Minute),
		updatedAt: now.Add(-20 * time.Minute),
	})

	claimed, err := reaper.ClaimOnce(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed != 1 {
		t.Fatalf("claim = %d, want 1", claimed)
	}
	if got, want := remainingIDs(t, ctx, conn), []string{"sec_young_orphan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after tick = %v, want %v (older reclaimable active reaped first)", got, want)
	}
}
