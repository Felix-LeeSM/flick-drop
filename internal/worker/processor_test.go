package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
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

	if _, err := store.Start(ctx, "job_processing", events.KindDeleteSecret, string(testJobPayload(t, "job_processing")), DefaultMaxAttempts); err != nil {
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

// Given the default slog routes to a JSON handler over a buffer,
// when a job handler fails to its retry limit,
// then an Error record carries the job kind and a goroutine stack.
func TestProcessorFailedJobLogsErrorWithStack(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	// Mirror production wiring: cmd/flick-worker sets the default logger from
	// telemetry.NewLogger, whose handler chain includes the stack decorator.
	slog.SetDefault(slog.New(telemetry.NewStackHandler(slog.NewJSONHandler(&buf, nil))))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	handlerErr := errors.New("failed permanently")
	handler := &fakeJobHandler{err: handlerErr}
	processor := newTestProcessor(t, store, handler, 1)

	result, err := processor.Process(ctx, testJobPayload(t, "job_error_log"))
	if err != nil {
		t.Fatalf("process terminal failure: %v", err)
	}
	if !result.Failed || !result.DeadLettered {
		t.Fatalf("result = %+v, want failed dead-lettered", result)
	}

	line := strings.TrimSpace(buf.String())
	var rec struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
		Kind  string `json:"kind"`
		Err   string `json:"err"`
		Stack string `json:"stack"`
	}
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, line)
	}
	if rec.Level != "ERROR" || rec.Msg != "worker job failed" {
		t.Fatalf("log record = %q/%q, want ERROR/worker job failed", rec.Level, rec.Msg)
	}
	if rec.Kind != string(events.KindDeleteSecret) {
		t.Fatalf("log kind = %q, want %q", rec.Kind, events.KindDeleteSecret)
	}
	if rec.Err != handlerErr.Error() {
		t.Fatalf("log err = %q, want %q", rec.Err, handlerErr.Error())
	}
	if !strings.Contains(rec.Stack, "goroutine ") {
		t.Fatalf("log record stack is not a goroutine stack: %q", rec.Stack)
	}
}

// 열람된 객체의 삭제가 실패해도 worker.db(시도·영수증·데드레터)와 로그에 객체 키나
// 시크릿 ID가 남지 않는다. 버킷 SDK 오류 문자열이 키를 품어도 가려진다.
func TestFailedOpenedObjectDeleteStoresAndLogsNoKey(t *testing.T) {
	// Given: 오류 문구에 키를 담는 버킷과 열람 뒤 삭제 작업
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	const secretID = "SYNTHsecretIDforLeakCheck"
	key := "managed/secrets/" + secretID
	bucket := &fakeObjectDeleter{err: errors.New(`Delete "https://bucket.example/` + key + `": dial tcp: connection refused`)}
	handler, err := NewCleanupHandler(&fakeCleanupAPI{}, bucket, nil)
	if err != nil {
		t.Fatal(err)
	}
	ack := acknowledgeFunc(func(context.Context, events.JobEvent) error { return nil })
	processor, err := NewProcessor(store, handler, ProcessorOptions{MaxAttempts: 2, Acknowledger: ack})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := (events.JobEvent{JobID: "job_opened_delete", Kind: events.KindDeleteOCIObject, ObjectKey: key,
		Backend: "s3_object", Reason: events.ReasonConsumed, RequestedAt: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)}).JSON()
	if err != nil {
		t.Fatal(err)
	}

	// When: 재시도 한도까지 실패해 데드레터가 된다
	for range 2 {
		_, _ = processor.Process(ctx, payload)
		if _, err := store.db.ExecContext(ctx, `update job_receipts set updated_at = '2000-01-01T00:00:00Z'`); err != nil {
			t.Fatal(err)
		}
	}

	// Then: 저장된 행 어디에도, 로그에도 ID가 없고 데드레터는 남아 있다
	var stored string
	if err := store.db.QueryRowContext(ctx, `select
		(select group_concat(coalesce(last_error, '') || state) from job_receipts) ||
		(select group_concat(coalesce(error, '')) from job_attempts) ||
		(select group_concat(payload_json || error) from dead_letters)`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, StateDead) || !strings.Contains(stored, "job_opened_delete") {
		t.Fatalf("job did not dead-letter: %s", stored)
	}
	for name, text := range map[string]string{"worker.db": stored, "log": buf.String()} {
		if strings.Contains(text, secretID) {
			t.Fatalf("%s keeps the opened secret ID: %s", name, text)
		}
	}
	if !strings.Contains(buf.String(), "connection refused") {
		t.Fatalf("log lost the failure cause: %s", buf.String())
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

	_, err := processor.Process(ctx, []byte(`{"job_id":"job_bad","kind":"bogus_kind","requested_at":"2026-06-17T12:00:00Z","payload":{"passphrase":"nope"}}`))
	if !errors.Is(err, events.ErrInvalidEvent) {
		t.Fatalf("process invalid payload error = %v, want ErrInvalidEvent", err)
	}
	if len(handler.calls) != 0 {
		t.Fatalf("handler calls = %d, want 0", len(handler.calls))
	}
}

func TestProcessorProcessMessageReturnsTerminateForDeadLetter(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	handler := &fakeJobHandler{err: errors.New("failed permanently")}
	processor := newTestProcessor(t, store, handler, 1)

	action, err := processor.ProcessMessage(ctx, testJobPayload(t, "job_dead_action"))
	if err != nil {
		t.Fatalf("process message: %v", err)
	}
	if action != events.MessageTerminate {
		t.Fatalf("action = %q, want terminate", action)
	}
}

func TestProcessorProcessMessageReturnsRetryableError(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	handlerErr := errors.New("api unavailable")
	handler := &fakeJobHandler{err: handlerErr}
	processor := newTestProcessor(t, store, handler, 3)

	action, err := processor.ProcessMessage(ctx, testJobPayload(t, "job_retry_action"))
	if !errors.Is(err, handlerErr) {
		t.Fatalf("process message error = %v, want handler error", err)
	}
	if action != "" {
		t.Fatalf("action = %q, want empty action for retryable error", action)
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
		Kind:        events.KindDeleteSecret,
		SecretID:    "sec_" + jobID,
		Reason:      events.ReasonExpired,
		RequestedAt: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
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
