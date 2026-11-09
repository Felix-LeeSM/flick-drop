package requests

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/Felix-LeeSM/flick-drop/internal/usage"
)

func usageCount(t *testing.T, conn *sql.DB, day, metric string) int64 {
	t.Helper()
	var count int64
	err := conn.QueryRow(`select count from usage_counters where day = ? and metric = ?`, day, metric).Scan(&count)
	if err == sql.ErrNoRows {
		return 0
	}
	if err != nil {
		t.Fatalf("read usage counter %s/%s: %v", day, metric, err)
	}
	return count
}

func requireUsageCount(t *testing.T, conn *sql.DB, day, metric string, want int64) {
	t.Helper()
	if got := usageCount(t, conn, day, metric); got != want {
		t.Fatalf("usage counter %s/%s = %d, want %d", metric, day, got, want)
	}
}

// Each lifecycle transition must commit its counter with the state change:
// create rides the insert tx, submit rides the waiting->submitted tx, and
// consume rides the consuming tx.
func TestUsageCountersFollowRequestLifecycle(t *testing.T) {
	f := newFixture(t)
	day := usage.Day(f.now)
	ctx := context.Background()

	c := f.create(t)
	requireUsageCount(t, f.db, day, usage.RequestCreated, 1)

	in := submission("text")
	if _, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in); err != nil {
		t.Fatalf("submit: %v", err)
	}
	requireUsageCount(t, f.db, day, usage.RequestSubmitted, 1)

	// A retried identical submission is accepted from the replay branch without
	// a new state change, so the counter must not move.
	if _, err := f.store.Submit(ctx, c.ID, c.SubmissionToken, in); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	requireUsageCount(t, f.db, day, usage.RequestSubmitted, 1)

	opened, err := f.store.Open(ctx, c.ID, c.RetrievalToken)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened.ObjectKey != "" {
		t.Fatalf("inline submission opened with object key %q", opened.ObjectKey)
	}
	requireUsageCount(t, f.db, day, usage.RequestConsumed, 1)

	// Opening again conflicts without consuming twice.
	if _, err := f.store.Open(ctx, c.ID, c.RetrievalToken); !errors.Is(err, ErrConflict) {
		t.Fatalf("reopen error = %v, want ErrConflict", err)
	}
	requireUsageCount(t, f.db, day, usage.RequestConsumed, 1)
}
