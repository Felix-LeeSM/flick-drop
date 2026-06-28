package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/db"
)

func TestOutboxStoreEnqueueListAndMark(t *testing.T) {
	ctx := context.Background()
	conn := openEventsTestDB(t, ctx)
	store := newTestOutboxStore(t, conn)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	store.SetNowForTest(func() time.Time { return now })

	event := JobEvent{
		JobID:       "job_1",
		Kind:        KindExpireSecret,
		SecretID:    "sec_1",
		Reason:      ReasonExpired,
		RequestedAt: now,
	}
	record, err := store.Enqueue(ctx, event)
	if err != nil {
		t.Fatalf("enqueue event: %v", err)
	}
	if record.ID != event.JobID {
		t.Fatalf("record id = %q, want %q", record.ID, event.JobID)
	}
	assertPayloadSafe(t, record.PayloadJSON)

	due, err := store.ListDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due count = %d, want 1", len(due))
	}
	if due[0].Payload.SecretID != "sec_1" {
		t.Fatalf("secret id = %q, want sec_1", due[0].Payload.SecretID)
	}

	nextAttempt := now.Add(5 * time.Minute)
	if err := store.MarkFailed(ctx, event.JobID, errors.New("nats unavailable"), nextAttempt); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	due, err = store.ListDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due after failure: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due count after failure = %d, want 0", len(due))
	}

	due, err = store.ListDue(ctx, nextAttempt, 10)
	if err != nil {
		t.Fatalf("list due at retry time: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("retry due count = %d, want 1", len(due))
	}
	if due[0].State != StateFailed || due[0].Attempts != 1 {
		t.Fatalf("retry state = %q attempts = %d, want failed/1", due[0].State, due[0].Attempts)
	}
	if due[0].LastError == nil || *due[0].LastError != "nats unavailable" {
		t.Fatalf("last error = %v, want nats unavailable", due[0].LastError)
	}

	store.SetNowForTest(func() time.Time { return nextAttempt })
	if err := store.MarkPublished(ctx, event.JobID); err != nil {
		t.Fatalf("mark published: %v", err)
	}
	due, err = store.ListDue(ctx, nextAttempt, 10)
	if err != nil {
		t.Fatalf("list due after publish: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due count after publish = %d, want 0", len(due))
	}
}

func TestOutboxStoreRejectsPayloadJSONWithUnknownFields(t *testing.T) {
	ctx := context.Background()
	conn := openEventsTestDB(t, ctx)
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	_, err := conn.ExecContext(ctx, `insert into outbox_events (
		id, subject, payload_json, next_attempt_at, created_at, updated_at
	) values (?, ?, ?, ?, ?, ?)`,
		"job_unsafe",
		"flick.jobs",
		`{"job_id":"job_unsafe","kind":"expire_secret","secret_id":"sec_1","requested_at":"2026-06-17T10:00:00Z","payload":{"passphrase":"do-not-send"}}`,
		formatTime(now),
		formatTime(now),
		formatTime(now),
	)
	if err != nil {
		t.Fatalf("insert unsafe outbox event: %v", err)
	}

	store := newTestOutboxStore(t, conn)
	if _, err := store.ListDue(ctx, now, 10); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("list due error = %v, want ErrInvalidEvent", err)
	}
}

func TestJobEventRejectsMissingRequiredFields(t *testing.T) {
	event := JobEvent{
		RequestedAt: time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC),
	}

	if _, err := event.JSON(); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("event json error = %v, want ErrInvalidEvent", err)
	}
}

func TestOutboxStoreMarkMissingEvent(t *testing.T) {
	ctx := context.Background()
	store := newTestOutboxStore(t, openEventsTestDB(t, ctx))

	err := store.MarkPublished(ctx, "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("mark missing error = %v, want ErrNotFound", err)
	}
}

func openEventsTestDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()

	conn, err := db.OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	if err := db.MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("migrate api: %v", err)
	}
	return conn
}

func newTestOutboxStore(t *testing.T, conn *sql.DB) *OutboxStore {
	t.Helper()

	store, err := NewOutboxStore(conn, "flick.jobs")
	if err != nil {
		t.Fatalf("new outbox store: %v", err)
	}
	return store
}

func assertPayloadSafe(t *testing.T, payloadJSON string) {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("decode payload json: %v", err)
	}
	forbidden := []string{"payload", "plaintext", "passphrase", "derived_key", "decrypt_key", "ciphertext_body"}
	for _, key := range forbidden {
		if _, ok := payload[key]; ok {
			t.Fatalf("payload contains forbidden key %q", key)
		}
	}
}
