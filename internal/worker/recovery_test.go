package worker

import (
	"context"
	"errors"
	"testing"
	"time"

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
			database := openTestWorkerDatabase(t)
			store := openRecoveryStore(t, database)
			now := time.Now().UTC()
			store.SetNowForTest(func() time.Time { return now })
			event := events.JobEvent{JobID: "job_reopen", Kind: events.KindDeleteOCIObject, Backend: testBackend,
				ObjectKey: "object-reopen", Reason: events.ReasonExpired, RequestedAt: now}
			payload, err := event.JSON()
			if err != nil {
				t.Fatal(err)
			}
			started, err := store.Start(ctx, event.JobID, event.Kind, string(payload), DefaultMaxAttempts)
			if err != nil {
				t.Fatal(err)
			}
			objects := &fakeObjectDeleter{}
			handler, err := NewCleanupHandler(&fakeCleanupAPI{}, map[string]ObjectDeleter{testBackend: objects})
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

			store = openRecoveryStore(t, database)
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
	database := openTestWorkerDatabase(t)
	first, second := openRecoveryStore(t, database), openRecoveryStore(t, database)
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
		if _, err := store.Start(ctx, "job_interrupted", events.KindDeleteSecret, string(testJobPayload(t, "job_interrupted")), DefaultMaxAttempts); err != nil {
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
	if _, err := store.Start(ctx, "job_collision", events.KindDeleteOCIObject, string(testJobPayload(t, "job_collision")), DefaultMaxAttempts); err != nil {
		t.Fatal(err)
	}
	handler := &fakeJobHandler{}
	action, err := newTestProcessor(t, store, handler, 3).ProcessMessage(ctx, testJobPayload(t, "job_collision"))
	if err != nil || action != events.MessageTerminate || len(handler.calls) != 0 {
		t.Fatalf("invalid kind action = %q, error = %v, handler calls = %d", action, err, len(handler.calls))
	}
}

func TestProcessorRecoversPersistedFailureLimit(t *testing.T) {
	for _, processing := range []bool{false, true} {
		name := "final failure before dead letter"
		if processing {
			name = "older worker already claimed another attempt"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			database := openTestWorkerDatabase(t)
			store := openRecoveryStore(t, database)
			now := time.Now().UTC()
			store.SetNowForTest(func() time.Time { return now })
			payload := string(testJobPayload(t, "job_final_failure"))
			// Recreates the rows an older split transition left: three failures, no terminal receipt.
			for range DefaultMaxAttempts {
				started, err := store.Start(ctx, "job_final_failure", events.KindDeleteSecret, payload, 4)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.MarkFailed(ctx, started.Attempt.ID, errors.New("recorded failure"), payload, 4); err != nil {
					t.Fatal(err)
				}
			}
			if processing {
				if _, err := store.Start(ctx, "job_final_failure", events.KindDeleteSecret, payload, 4); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.db.Close(); err != nil {
				t.Fatal(err)
			}
			store = openRecoveryStore(t, database)
			store.SetNowForTest(func() time.Time { return now })
			handler := &fakeJobHandler{}
			processor := newTestProcessor(t, store, handler, DefaultMaxAttempts)
			if processing {
				if action, err := processor.ProcessMessage(ctx, []byte(payload)); err != nil || action != events.MessageRetry {
					t.Fatalf("live legacy attempt: action = %q, error = %v", action, err)
				}
				now = now.Add(ProcessingLease)
			}
			for range 2 {
				if action, err := processor.ProcessMessage(ctx, []byte(payload)); err != nil || action != events.MessageTerminate {
					t.Fatalf("exhausted budget: action = %q, error = %v", action, err)
				}
			}
			receipt, err := store.Receipt(ctx, "job_final_failure")
			if err != nil || receipt.State != StateDead || len(handler.calls) != 0 {
				t.Fatalf("receipt = %+v, handler calls = %d, error = %v", receipt, len(handler.calls), err)
			}
			attempts, err := store.Attempts(ctx, "job_final_failure")
			wantAttempts := 3
			if processing {
				wantAttempts = 4
			}
			if err != nil || len(attempts) != wantAttempts || receipt.Attempts != wantAttempts {
				t.Fatalf("recovery added an attempt: receipt = %+v, attempts = %+v, error = %v", receipt, attempts, err)
			}
			if processing && (attempts[3].Result != AttemptFailed || attempts[3].Error != nil) {
				t.Fatalf("interrupted legacy attempt = %+v", attempts[3])
			}
			dead, err := store.DeadLetterRecord(ctx, "job_final_failure")
			if err != nil || dead.PayloadJSON != payload || dead.Error != "recorded failure" {
				t.Fatalf("dead letter = %+v, error = %v", dead, err)
			}
		})
	}
}

func TestFinalFailureCommitsAtomicallyBeforeAnotherStart(t *testing.T) {
	ctx := context.Background()
	database := openTestWorkerDatabase(t)
	store, duplicate := openRecoveryStore(t, database), openRecoveryStore(t, database)
	payload := string(testJobPayload(t, "job_atomic"))
	var started StartResult
	for failure := 0; failure < DefaultMaxAttempts; failure++ {
		var err error
		started, err = store.Start(ctx, "job_atomic", events.KindDeleteSecret, payload, DefaultMaxAttempts)
		if err != nil {
			t.Fatal(err)
		}
		if failure < DefaultMaxAttempts-1 {
			if _, err := store.MarkFailed(ctx, started.Attempt.ID, errors.New("temporary failure"), payload, DefaultMaxAttempts); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The trigger fails the dead-letter write inside the transaction; the terminal receipt must roll back.
	if _, err := store.db.ExecContext(ctx, `create function reject_dead_letter() returns trigger
		language plpgsql as $fn$
		begin
			raise exception 'injected dead-letter write failure';
		end;
		$fn$`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `create trigger reject_dead_letter before insert on dead_letters
		for each row execute function reject_dead_letter()`); err != nil {
		t.Fatal(err)
	}
	if dead, err := store.MarkFailed(ctx, started.Attempt.ID, errors.New("final failure"), payload, DefaultMaxAttempts); err == nil || dead {
		t.Fatalf("injected storage failure: dead = %v, error = %v", dead, err)
	}
	attempts, err := store.Attempts(ctx, "job_atomic")
	if err != nil || len(attempts) != 3 || attempts[2].Result != AttemptRunning || attempts[2].Error != nil {
		t.Fatalf("final failure was partially committed: attempts = %+v, error = %v", attempts, err)
	}
	if _, err := duplicate.Start(ctx, "job_atomic", events.KindDeleteSecret, payload, DefaultMaxAttempts); !errors.Is(err, ErrJobProcessing) {
		t.Fatalf("rolled-back failure allowed another attempt: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `drop trigger reject_dead_letter on dead_letters`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `drop function reject_dead_letter`); err != nil {
		t.Fatal(err)
	}
	duplicateDone := make(chan error, 1)
	go func() {
		_, err := duplicate.Start(ctx, "job_atomic", events.KindDeleteSecret, payload, DefaultMaxAttempts)
		duplicateDone <- err
	}()
	if dead, err := store.MarkFailed(ctx, started.Attempt.ID, errors.New("final failure"), payload, DefaultMaxAttempts); err != nil || !dead {
		t.Fatalf("commit final failure: dead = %v, error = %v", dead, err)
	}
	if err := <-duplicateDone; !errors.Is(err, ErrJobProcessing) && !errors.Is(err, ErrJobDead) {
		t.Fatalf("concurrent start error = %v", err)
	}
	receipt, err := store.Receipt(ctx, "job_atomic")
	if err != nil || receipt.State != StateDead || receipt.Attempts != 3 || receipt.CompletedAt == nil {
		t.Fatalf("terminal receipt = %+v, error = %v", receipt, err)
	}
	dead, err := store.DeadLetterRecord(ctx, "job_atomic")
	if err != nil || dead.PayloadJSON != payload || dead.Error != "final failure" {
		t.Fatalf("dead letter = %+v, error = %v", dead, err)
	}
}

func TestStaleFinalFailureCannotTerminateNewerAttempt(t *testing.T) {
	ctx := context.Background()
	database := openTestWorkerDatabase(t)
	old, current := openRecoveryStore(t, database), openRecoveryStore(t, database)
	now := time.Now().UTC()
	old.SetNowForTest(func() time.Time { return now })
	current.SetNowForTest(func() time.Time { return now.Add(ProcessingLease) })
	payload := string(testJobPayload(t, "job_stale_failure"))
	first, err := old.Start(ctx, "job_stale_failure", events.KindDeleteSecret, payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := current.Start(ctx, "job_stale_failure", events.KindDeleteSecret, payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dead, err := old.MarkFailed(ctx, first.Attempt.ID, errors.New("late failure"), payload, 1); !errors.Is(err, ErrStaleAttempt) || dead {
		t.Fatalf("stale final failure: dead = %v, error = %v", dead, err)
	}
	if err := old.MarkSucceeded(ctx, first.Attempt.ID); !errors.Is(err, ErrStaleAttempt) {
		t.Fatalf("stale success: %v", err)
	}
	if _, err := current.DeadLetterRecord(ctx, "job_stale_failure"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale failure created a dead letter: %v", err)
	}
	if err := current.MarkSucceeded(ctx, second.Attempt.ID); err != nil {
		t.Fatalf("newer attempt could not finish: %v", err)
	}
}
