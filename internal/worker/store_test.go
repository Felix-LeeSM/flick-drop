package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// 단일 경쟁은 우연히 통과하기 쉬워 라운드마다 리스를 다시 만료시키며 반복한다.
func TestReceiptStoreStartClaimsExpiredLeaseExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	payload := string(testJobPayload(t, "job_race"))

	first, err := store.Start(ctx, "job_race", "delete_secret", payload, DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("first start: %v", err)
	}

	const rounds = 25
	const racers = 4
	clock := now
	for round := 1; round <= rounds; round++ {
		// 승자가 updated_at을 자기 now로 쓰므로 같은 라운드의 뒤 클레임은 신선한 리스를 본다.
		clock = clock.Add(2 * ProcessingLease)
		roundClock := clock
		store.SetNowForTest(func() time.Time { return roundClock })

		var wg sync.WaitGroup
		results := make(chan error, racers)
		var claimed sync.Mutex
		claimedCount := 0
		for range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				started, err := store.Start(ctx, "job_race", "delete_secret", payload, DefaultMaxAttempts)
				if err == nil {
					claimed.Lock()
					claimedCount++
					claimed.Unlock()
					if got, want := started.Attempt.Attempt, round+1; got != want {
						t.Errorf("round %d: winner attempt number = %d, want %d", round, got, want)
					}
				}
				results <- err
			}()
		}
		wg.Wait()
		close(results)

		for err := range results {
			if err != nil && !errors.Is(err, ErrJobProcessing) {
				t.Fatalf("round %d: racer error = %v, want nil or ErrJobProcessing", round, err)
			}
		}
		if claimedCount != 1 {
			t.Fatalf("round %d: expired-lease claim won %d times, want exactly 1", round, claimedCount)
		}
	}

	if err := store.MarkSucceeded(ctx, first.Attempt.ID); !errors.Is(err, ErrStaleAttempt) {
		t.Fatalf("original attempt finish = %v, want ErrStaleAttempt", err)
	}
}

func TestReceiptStoreStartSucceedAndDuplicate(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	started, err := store.Start(ctx, "job_1", "delete_secret", string(testJobPayload(t, "job_1")), DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("start job: %v", err)
	}
	if started.AlreadySucceeded {
		t.Fatal("first start unexpectedly already succeeded")
	}
	if started.Attempt.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", started.Attempt.Attempt)
	}

	store.SetNowForTest(func() time.Time { return now.Add(time.Minute) })
	if err := store.MarkSucceeded(ctx, started.Attempt.ID); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}

	duplicate, err := store.Start(ctx, "job_1", "delete_secret", string(testJobPayload(t, "job_1")), DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("duplicate start: %v", err)
	}
	if !duplicate.AlreadySucceeded {
		t.Fatal("duplicate start should report already succeeded")
	}

	receipt, err := store.Receipt(ctx, "job_1")
	if err != nil {
		t.Fatalf("load receipt: %v", err)
	}
	if receipt.State != StateSucceeded || receipt.Attempts != 1 {
		t.Fatalf("receipt state = %q attempts = %d, want succeeded/1", receipt.State, receipt.Attempts)
	}
	if receipt.CompletedAt == nil {
		t.Fatal("expected completed_at")
	}

	attempts, err := store.Attempts(ctx, "job_1")
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempt count = %d, want 1", len(attempts))
	}
	if attempts[0].Result != AttemptSucceeded {
		t.Fatalf("attempt result = %q, want succeeded", attempts[0].Result)
	}
}

func TestReceiptStoreRejectsDuplicateWhileProcessing(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)

	started, err := store.Start(ctx, "job_processing", "delete_secret", string(testJobPayload(t, "job_processing")), DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("start job: %v", err)
	}
	if started.Attempt.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", started.Attempt.Attempt)
	}

	_, err = store.Start(ctx, "job_processing", "delete_secret", string(testJobPayload(t, "job_processing")), DefaultMaxAttempts)
	if !errors.Is(err, ErrJobProcessing) {
		t.Fatalf("duplicate processing error = %v, want ErrJobProcessing", err)
	}

	receipt, err := store.Receipt(ctx, "job_processing")
	if err != nil {
		t.Fatalf("load receipt: %v", err)
	}
	if receipt.State != StateProcessing || receipt.Attempts != 1 {
		t.Fatalf("receipt state = %q attempts = %d, want processing/1", receipt.State, receipt.Attempts)
	}

	attempts, err := store.Attempts(ctx, "job_processing")
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempt count = %d, want 1", len(attempts))
	}
}

func TestReceiptStoreFailedAttemptCanRetry(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	first, err := store.Start(ctx, "job_retry", "delete_secret", string(testJobPayload(t, "job_retry")), DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("start first attempt: %v", err)
	}
	if _, err := store.MarkFailed(ctx, first.Attempt.ID, errors.New("temporary outage"), string(testJobPayload(t, "job_retry")), DefaultMaxAttempts); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	store.SetNowForTest(func() time.Time { return now.Add(time.Minute) })
	second, err := store.Start(ctx, "job_retry", "delete_secret", string(testJobPayload(t, "job_retry")), DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("start second attempt: %v", err)
	}
	if second.Attempt.Attempt != 2 {
		t.Fatalf("second attempt = %d, want 2", second.Attempt.Attempt)
	}

	receipt, err := store.Receipt(ctx, "job_retry")
	if err != nil {
		t.Fatalf("load receipt: %v", err)
	}
	if receipt.State != StateProcessing || receipt.Attempts != 2 {
		t.Fatalf("receipt state = %q attempts = %d, want processing/2", receipt.State, receipt.Attempts)
	}

	attempts, err := store.Attempts(ctx, "job_retry")
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want 2", len(attempts))
	}
	if attempts[0].Result != AttemptFailed || attempts[1].Result != AttemptRunning {
		t.Fatalf("attempt results = %q/%q, want failed/running", attempts[0].Result, attempts[1].Result)
	}
}

func TestReceiptStoreRejectsStaleAttemptFinish(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	_, err := store.db.ExecContext(ctx, `insert into job_receipts (
		job_id, kind, state, attempts, first_seen_at, updated_at
	) values ($1, $2, $3, 2, $4, $5)`,
		"job_stale",
		"delete_secret",
		StateProcessing,
		now,
		now,
	)
	if err != nil {
		t.Fatalf("insert receipt: %v", err)
	}
	var staleAttemptID int64
	if err := store.db.QueryRowContext(ctx, `insert into job_attempts (
		job_id, attempt, started_at, result
	) values ($1, 1, $2, $3)
	returning id`,
		"job_stale",
		now,
		AttemptRunning,
	).Scan(&staleAttemptID); err != nil {
		t.Fatalf("insert stale attempt: %v", err)
	}
	var currentAttemptID int64
	if err := store.db.QueryRowContext(ctx, `insert into job_attempts (
		job_id, attempt, started_at, result
	) values ($1, 2, $2, $3)
	returning id`,
		"job_stale",
		now.Add(time.Minute),
		AttemptRunning,
	).Scan(&currentAttemptID); err != nil {
		t.Fatalf("insert current attempt: %v", err)
	}

	store.SetNowForTest(func() time.Time { return now.Add(2 * time.Minute) })
	if err := store.MarkSucceeded(ctx, currentAttemptID); err != nil {
		t.Fatalf("mark current succeeded: %v", err)
	}
	if _, err := store.MarkFailed(ctx, staleAttemptID, errors.New("late failure"), string(testJobPayload(t, "job_stale")), DefaultMaxAttempts); !errors.Is(err, ErrStaleAttempt) {
		t.Fatalf("mark stale failed error = %v, want ErrStaleAttempt", err)
	}

	receipt, err := store.Receipt(ctx, "job_stale")
	if err != nil {
		t.Fatalf("load receipt: %v", err)
	}
	if receipt.State != StateSucceeded {
		t.Fatalf("receipt state = %q, want succeeded", receipt.State)
	}
}

func TestReceiptStoreRejectsMismatchedKind(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)

	if _, err := store.Start(ctx, "job_kind", "delete_oci_object", string(testJobPayload(t, "job_kind")), DefaultMaxAttempts); err != nil {
		t.Fatalf("start first job: %v", err)
	}

	_, err := store.Start(ctx, "job_kind", "delete_secret", string(testJobPayload(t, "job_kind")), DefaultMaxAttempts)
	if !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("mismatched kind error = %v, want ErrInvalidJob", err)
	}
}

func TestReceiptStoreDeadLetter(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	payload := `{"job_id":"job_dead","kind":"delete_secret","secret_id":"sec_1","requested_at":"2026-06-17T12:00:00Z"}`
	started, err := store.Start(ctx, "job_dead", "delete_secret", payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dead, err := store.MarkFailed(ctx, started.Attempt.ID, errors.New("failed permanently"), payload, 1); err != nil || !dead {
		t.Fatalf("dead letter: %v", err)
	}

	receipt, err := store.Receipt(ctx, "job_dead")
	if err != nil {
		t.Fatalf("load receipt: %v", err)
	}
	if receipt.State != StateDead {
		t.Fatalf("receipt state = %q, want dead", receipt.State)
	}
	if receipt.LastError == nil || *receipt.LastError != "failed permanently" {
		t.Fatalf("last error = %v, want failed permanently", receipt.LastError)
	}
	if _, err := store.Start(ctx, "job_dead", "delete_secret", string(testJobPayload(t, "job_dead")), DefaultMaxAttempts); !errors.Is(err, ErrJobDead) {
		t.Fatalf("start dead job error = %v, want ErrJobDead", err)
	}

	dead, err := store.DeadLetterRecord(ctx, "job_dead")
	if err != nil {
		t.Fatalf("load dead letter: %v", err)
	}
	if dead.PayloadJSON != payload {
		t.Fatalf("payload json = %q, want %q", dead.PayloadJSON, payload)
	}
	if dead.Error != "failed permanently" {
		t.Fatalf("dead letter error = %q, want failed permanently", dead.Error)
	}
}

func TestReceiptStoreDeadLetterDoesNotOverwriteSucceededJob(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)

	started, err := store.Start(ctx, "job_succeeded", "delete_secret", string(testJobPayload(t, "job_succeeded")), DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("start job: %v", err)
	}
	if err := store.MarkSucceeded(ctx, started.Attempt.ID); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}

	_, err = store.MarkFailed(ctx, started.Attempt.ID, errors.New("late failure"), string(testJobPayload(t, "job_succeeded")), 1)
	if !errors.Is(err, ErrStaleAttempt) {
		t.Fatalf("dead letter succeeded job error = %v, want ErrStaleAttempt", err)
	}

	receipt, err := store.Receipt(ctx, "job_succeeded")
	if err != nil {
		t.Fatalf("load receipt: %v", err)
	}
	if receipt.State != StateSucceeded {
		t.Fatalf("receipt state = %q, want succeeded", receipt.State)
	}
}

func newTestReceiptStore(t *testing.T, ctx context.Context) *ReceiptStore {
	t.Helper()

	return openRecoveryStore(t, openTestWorkerDatabase(t))
}
