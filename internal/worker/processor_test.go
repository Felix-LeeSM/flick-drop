package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Felix-LeeSM/burn-links/internal/events"
)

func TestProcessorMarksSuccessfulJob(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	handler := &fakeJobHandler{}
	processor := newTestProcessor(t, store, handler, 3)
	payload := testJobPayload(t, "job_success")

	result, err := processor.Process(ctx, payload)
	if err != nil {
		t.Fatalf("process job: %v", err)
	}
	if !result.Started || !result.Succeeded || result.Attempt != 1 {
		t.Fatalf("result = %+v, want started succeeded attempt 1", result)
	}
	if len(handler.calls) != 1 {
		t.Fatalf("handler calls = %d, want 1", len(handler.calls))
	}

	receipt, err := store.Receipt(ctx, "job_success")
	if err != nil {
		t.Fatalf("load receipt: %v", err)
	}
	if receipt.State != StateSucceeded || receipt.Attempts != 1 {
		t.Fatalf("receipt state = %q attempts = %d, want succeeded/1", receipt.State, receipt.Attempts)
	}
}

func TestProcessorSkipsAlreadySucceededJob(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	handler := &fakeJobHandler{}
	processor := newTestProcessor(t, store, handler, 3)
	payload := testJobPayload(t, "job_done")

	if _, err := processor.Process(ctx, payload); err != nil {
		t.Fatalf("process first job: %v", err)
	}
	handler.err = errors.New("should not run")
	result, err := processor.Process(ctx, payload)
	if err != nil {
		t.Fatalf("process duplicate job: %v", err)
	}
	if !result.AlreadySucceeded || !result.Succeeded {
		t.Fatalf("result = %+v, want already succeeded", result)
	}
	if len(handler.calls) != 1 {
		t.Fatalf("handler calls = %d, want 1", len(handler.calls))
	}
}

func TestProcessorSkipsDuplicateProcessingJob(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	handler := &fakeJobHandler{}
	processor := newTestProcessor(t, store, handler, 3)

	if _, err := store.Start(ctx, "job_processing", events.KindExpireSecret); err != nil {
		t.Fatalf("start existing job: %v", err)
	}
	result, err := processor.Process(ctx, testJobPayload(t, "job_processing"))
	if err != nil {
		t.Fatalf("process duplicate processing job: %v", err)
	}
	if !result.AlreadyProcessing {
		t.Fatalf("result = %+v, want already processing", result)
	}
	if len(handler.calls) != 0 {
		t.Fatalf("handler calls = %d, want 0", len(handler.calls))
	}

	attempts, err := store.Attempts(ctx, "job_processing")
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempt count = %d, want 1", len(attempts))
	}
}

func TestProcessorMarksFailedJobForRetry(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	handlerErr := errors.New("api unavailable")
	handler := &fakeJobHandler{err: handlerErr}
	processor := newTestProcessor(t, store, handler, 3)

	result, err := processor.Process(ctx, testJobPayload(t, "job_retry"))
	if !errors.Is(err, handlerErr) {
		t.Fatalf("process error = %v, want handler error", err)
	}
	if !result.Started || !result.Failed || result.DeadLettered {
		t.Fatalf("result = %+v, want failed retry", result)
	}

	receipt, err := store.Receipt(ctx, "job_retry")
	if err != nil {
		t.Fatalf("load receipt: %v", err)
	}
	if receipt.State != StateFailed || receipt.Attempts != 1 {
		t.Fatalf("receipt state = %q attempts = %d, want failed/1", receipt.State, receipt.Attempts)
	}
}

func TestProcessorDeadLettersAtRetryLimit(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	handlerErr := errors.New("failed permanently")
	handler := &fakeJobHandler{err: handlerErr}
	processor := newTestProcessor(t, store, handler, 1)
	payload := testJobPayload(t, "job_dead")

	result, err := processor.Process(ctx, payload)
	if err != nil {
		t.Fatalf("process terminal failure: %v", err)
	}
	if !result.Failed || !result.DeadLettered {
		t.Fatalf("result = %+v, want failed dead-lettered", result)
	}

	receipt, err := store.Receipt(ctx, "job_dead")
	if err != nil {
		t.Fatalf("load receipt: %v", err)
	}
	if receipt.State != StateDead || receipt.Attempts != 1 {
		t.Fatalf("receipt state = %q attempts = %d, want dead/1", receipt.State, receipt.Attempts)
	}
	dead, err := store.DeadLetterRecord(ctx, "job_dead")
	if err != nil {
		t.Fatalf("load dead letter: %v", err)
	}
	if dead.PayloadJSON != string(payload) {
		t.Fatalf("dead payload = %q, want %q", dead.PayloadJSON, string(payload))
	}
	if dead.Error != handlerErr.Error() {
		t.Fatalf("dead error = %q, want %q", dead.Error, handlerErr.Error())
	}
}

func TestProcessorRejectsInvalidPayload(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	handler := &fakeJobHandler{}
	processor := newTestProcessor(t, store, handler, 3)

	_, err := processor.Process(ctx, []byte(`{"job_id":"job_bad","kind":"expire_secret","requested_at":"2026-06-17T12:00:00Z","payload":{"passphrase":"nope"}}`))
	if !errors.Is(err, events.ErrInvalidEvent) {
		t.Fatalf("process invalid payload error = %v, want ErrInvalidEvent", err)
	}
	if len(handler.calls) != 0 {
		t.Fatalf("handler calls = %d, want 0", len(handler.calls))
	}
}

func newTestProcessor(t *testing.T, store *ReceiptStore, handler JobHandler, maxAttempts int) *Processor {
	t.Helper()

	processor, err := NewProcessor(store, handler, ProcessorOptions{MaxAttempts: maxAttempts})
	if err != nil {
		t.Fatalf("new processor: %v", err)
	}
	return processor
}

func testJobPayload(t *testing.T, jobID string) []byte {
	t.Helper()

	payload, err := (events.JobEvent{
		JobID:       jobID,
		Kind:        events.KindExpireSecret,
		SecretID:    "sec_" + jobID,
		Reason:      events.ReasonExpired,
		RequestedAt: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
		TraceID:     "trc_" + jobID,
	}).JSON()
	if err != nil {
		t.Fatalf("build job payload: %v", err)
	}
	return payload
}

type fakeJobHandler struct {
	calls []events.JobEvent
	err   error
}

func (h *fakeJobHandler) HandleJob(_ context.Context, event events.JobEvent) error {
	h.calls = append(h.calls, event)
	return h.err
}
