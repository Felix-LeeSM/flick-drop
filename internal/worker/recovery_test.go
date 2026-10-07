package worker

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
	"github.com/Felix-LeeSM/flick-drop/internal/events"
)

func TestProcessorRecoversAfterDatabaseReopen(t *testing.T) {
	for _, sideEffectCompleted := range []bool{false, true} {
		name := "before deletion"
		if sideEffectCompleted {
			name = "after deletion before receipt completion"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "worker.db")
			store := openRecoveryStore(t, path)
			now := time.Now().UTC()
			store.SetNowForTest(func() time.Time { return now })
			event := events.JobEvent{JobID: "job_reopen", Kind: events.KindDeleteOCIObject,
				ObjectKey: "object-reopen", Reason: events.ReasonExpired, RequestedAt: now}
			payload, err := event.JSON()
			if err != nil {
				t.Fatal(err)
			}
			started, err := store.Start(ctx, event.JobID, event.Kind)
			if err != nil {
				t.Fatal(err)
			}
			objects := &fakeObjectDeleter{}
			handler, err := NewCleanupHandler(&fakeCleanupAPI{}, objects)
			if err != nil {
				t.Fatal(err)
			}
			if sideEffectCompleted {
				if err := handler.HandleJob(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.db.Close(); err != nil {
				t.Fatal(err)
			}

			store = openRecoveryStore(t, path)
			store.SetNowForTest(func() time.Time { return now.Add(ProcessingLease) })
			processor := newTestProcessor(t, store, handler, 3)
			for range 2 { // The second delivery must not repeat a completed job.
				action, err := processor.ProcessMessage(ctx, payload)
				if err != nil || action != events.MessageAck {
					t.Fatalf("recovered action = %q, error = %v", action, err)
				}
			}
			wantDeletes := 1
			if sideEffectCompleted {
				wantDeletes++ // At-least-once replay after an uncertain outcome.
			}
			if len(objects.deleted) != wantDeletes {
				t.Fatalf("deletions = %v, want %d", objects.deleted, wantDeletes)
			}
			if err := store.MarkSucceeded(ctx, started.Attempt.ID); err == nil {
				t.Fatal("an interrupted attempt must not finish a newer attempt")
			}
		})
	}
}

func TestProcessorActiveDuplicateAcrossConnectionsRetries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "worker.db")
	first, second := openRecoveryStore(t, path), openRecoveryStore(t, path)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	handler := JobHandlerFunc(func(ctx context.Context, _ events.JobEvent) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > JobTimeout {
			t.Error("handler has no bounded deadline")
		}
		close(started)
		<-release
		return nil
	})
	processor := newTestProcessor(t, first, handler, 3)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := processor.ProcessMessage(ctx, testJobPayload(t, "job_active"))
		done <- err
	}()
	<-started
	duplicateCalls := 0
	duplicate := newTestProcessor(t, second, JobHandlerFunc(func(context.Context, events.JobEvent) error {
		duplicateCalls++
		return nil
	}), 3)
	message := &runnerMessage{data: testJobPayload(t, "job_active")}
	if _, err := events.ConsumeMessages(ctx, []events.Message{message}, duplicate); err != nil {
		t.Fatal(err)
	}
	if message.acked || !message.naked || message.terminated {
		t.Fatalf("active duplicate ack/nak/term = %v/%v/%v", message.acked, message.naked, message.terminated)
	}
	if duplicateCalls != 0 {
		t.Fatal("active duplicate executed the handler")
	}
	// A cancelled shutdown leaves a recoverable lease, even if the handler
	// finished its side effect just before observing cancellation.
	cancel()
	release <- struct{}{}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v", err)
	}
	second.SetNowForTest(func() time.Time { return time.Now().UTC().Add(ProcessingLease) })
	action, err := duplicate.ProcessMessage(context.Background(), message.data)
	if err != nil || action != events.MessageAck || duplicateCalls != 1 {
		t.Fatalf("recovered cancelled action = %q, error = %v", action, err)
	}
}

func TestInterruptedAttemptsDoNotSpendFailureBudget(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	now := time.Now().UTC()
	store.SetNowForTest(func() time.Time { return now })
	for range 4 {
		if _, err := store.Start(ctx, "job_interrupted", events.KindDeleteSecret); err != nil {
			t.Fatal(err)
		}
		now = now.Add(ProcessingLease)
	}
	handlerErr := errors.New("object store unavailable")
	processor := newTestProcessor(t, store, &fakeJobHandler{err: handlerErr}, 3)
	for failure := 1; failure <= 3; failure++ {
		result, err := processor.Process(ctx, testJobPayload(t, "job_interrupted"))
		if failure < 3 && (!errors.Is(err, handlerErr) || result.DeadLettered) {
			t.Fatalf("failure %d: result = %+v, error = %v", failure, result, err)
		}
		if failure == 3 && (err != nil || !result.DeadLettered) {
			t.Fatalf("final failure: result = %+v, error = %v", result, err)
		}
	}
}

func TestProcessorTerminatesMismatchedJobKind(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	if _, err := store.Start(ctx, "job_collision", events.KindDeleteOCIObject); err != nil {
		t.Fatal(err)
	}
	handler := &fakeJobHandler{}
	action, err := newTestProcessor(t, store, handler, 3).ProcessMessage(ctx, testJobPayload(t, "job_collision"))
	if err != nil || action != events.MessageTerminate || len(handler.calls) != 0 {
		t.Fatalf("invalid kind action = %q, error = %v, handler calls = %d", action, err, len(handler.calls))
	}
}

func openRecoveryStore(t *testing.T, path string) *ReceiptStore {
	t.Helper()
	conn, err := db.OpenSQLite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.MigrateWorker(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	store, err := NewReceiptStore(conn)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
