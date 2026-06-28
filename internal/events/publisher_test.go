package events

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestOutboxPublisherPublishesDueEvents(t *testing.T) {
	ctx := context.Background()
	conn := openEventsTestDB(t, ctx)
	store := newTestOutboxStore(t, conn)
	now := time.Date(2026, 6, 17, 11, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	event := JobEvent{
		JobID:       "job_publish",
		Kind:        KindDeleteSecret,
		SecretID:    "sec_publish",
		Reason:      ReasonExpired,
		RequestedAt: now,
	}
	record, err := store.Enqueue(ctx, event)
	if err != nil {
		t.Fatalf("enqueue event: %v", err)
	}

	fake := &fakeMessagePublisher{}
	publisher := newTestOutboxPublisher(t, store, fake, now)
	result, err := publisher.PublishDue(ctx, 10)
	if err != nil {
		t.Fatalf("publish due: %v", err)
	}
	if result.Published != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want 1 published and 0 failed", result)
	}
	if len(fake.messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(fake.messages))
	}
	if fake.messages[0].subject != "flick.jobs" {
		t.Fatalf("subject = %q, want flick.jobs", fake.messages[0].subject)
	}
	if !bytes.Equal(fake.messages[0].payload, []byte(record.PayloadJSON)) {
		t.Fatalf("published payload mismatch")
	}
	assertPayloadSafe(t, string(fake.messages[0].payload))

	due, err := store.ListDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due count = %d, want 0", len(due))
	}
}

func TestOutboxPublisherMarksFailedEventsForRetry(t *testing.T) {
	ctx := context.Background()
	conn := openEventsTestDB(t, ctx)
	store := newTestOutboxStore(t, conn)
	now := time.Date(2026, 6, 17, 11, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })
	event := JobEvent{
		JobID:       "job_fail",
		Kind:        KindDeleteSecret,
		SecretID:    "sec_fail",
		Reason:      ReasonExpired,
		RequestedAt: now,
	}
	if _, err := store.Enqueue(ctx, event); err != nil {
		t.Fatalf("enqueue event: %v", err)
	}

	fake := &fakeMessagePublisher{err: errors.New("publish unavailable")}
	publisher := newTestOutboxPublisher(t, store, fake, now)
	result, err := publisher.PublishDue(ctx, 10)
	if err != nil {
		t.Fatalf("publish due: %v", err)
	}
	if result.Published != 0 || result.Failed != 1 {
		t.Fatalf("result = %+v, want 0 published and 1 failed", result)
	}

	due, err := store.ListDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due before retry: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due count before retry = %d, want 0", len(due))
	}

	retryAt := now.Add(time.Minute)
	due, err = store.ListDue(ctx, retryAt, 10)
	if err != nil {
		t.Fatalf("list due at retry: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due count at retry = %d, want 1", len(due))
	}
	if due[0].State != StateFailed || due[0].Attempts != 1 {
		t.Fatalf("retry state = %q attempts = %d, want failed/1", due[0].State, due[0].Attempts)
	}
	if due[0].LastError == nil || *due[0].LastError != "publish unavailable" {
		t.Fatalf("last error = %v, want publish unavailable", due[0].LastError)
	}
}

func newTestOutboxPublisher(t *testing.T, store *OutboxStore, publisher MessagePublisher, now time.Time) *OutboxPublisher {
	t.Helper()

	outboxPublisher, err := NewOutboxPublisher(store, publisher, OutboxPublisherOptions{
		RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatalf("new outbox publisher: %v", err)
	}
	outboxPublisher.SetNowForTest(func() time.Time { return now })
	return outboxPublisher
}

type fakeMessagePublisher struct {
	err      error
	messages []publishedMessage
}

type publishedMessage struct {
	subject string
	payload []byte
}

func (p *fakeMessagePublisher) Publish(_ context.Context, subject string, payload []byte) error {
	if p.err != nil {
		return p.err
	}
	p.messages = append(p.messages, publishedMessage{
		subject: subject,
		payload: append([]byte(nil), payload...),
	})
	return nil
}
