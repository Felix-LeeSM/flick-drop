package events

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	StatePending   = "pending"
	StatePublished = "published"
	StateFailed    = "failed"
)

type OutboxStore struct {
	db      *sql.DB
	subject string
	now     func() time.Time
}

type OutboxRecord struct {
	ID            string
	Subject       string
	Payload       JobEvent
	PayloadJSON   string
	State         string
	Attempts      int
	NextAttemptAt time.Time
	PublishedAt   *time.Time
	LastError     *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func NewOutboxStore(db *sql.DB, subject string) (*OutboxStore, error) {
	if db == nil {
		return nil, fmt.Errorf("db is required")
	}
	if subject == "" {
		return nil, fmt.Errorf("subject is required")
	}
	return &OutboxStore{
		db:      db,
		subject: subject,
		now:     func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *OutboxStore) SetNowForTest(now func() time.Time) {
	s.now = now
}

func (s *OutboxStore) Enqueue(ctx context.Context, event JobEvent) (OutboxRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OutboxRecord{}, fmt.Errorf("begin enqueue outbox event: %w", err)
	}
	defer rollback(tx)

	record, err := s.EnqueueTx(ctx, tx, event)
	if err != nil {
		return OutboxRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return OutboxRecord{}, fmt.Errorf("commit enqueue outbox event: %w", err)
	}
	return record, nil
}

func (s *OutboxStore) EnqueueTx(ctx context.Context, tx *sql.Tx, event JobEvent) (OutboxRecord, error) {
	// Capture the enqueuing span's trace context into the payload now, while the
	// span is still active — the async publisher runs after it has ended (#133).
	injectTraceContext(ctx, &event)
	payloadJSON, err := event.JSON()
	if err != nil {
		return OutboxRecord{}, err
	}

	now := s.now().UTC()
	_, err = tx.ExecContext(ctx, `insert into outbox_events (
		id, subject, payload_json, state, attempts, next_attempt_at, created_at, updated_at
	) values (?, ?, ?, ?, 0, ?, ?, ?)`,
		event.JobID,
		s.subject,
		string(payloadJSON),
		StatePending,
		formatTime(now),
		formatTime(now),
		formatTime(now),
	)
	if err != nil {
		return OutboxRecord{}, fmt.Errorf("insert outbox event: %w", err)
	}

	return OutboxRecord{
		ID:            event.JobID,
		Subject:       s.subject,
		Payload:       event,
		PayloadJSON:   string(payloadJSON),
		State:         StatePending,
		Attempts:      0,
		NextAttemptAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (s *OutboxStore) ListDue(ctx context.Context, dueAt time.Time, limit int) ([]OutboxRecord, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive")
	}

	rows, err := s.db.QueryContext(ctx, `select
			id, subject, payload_json, state, attempts, next_attempt_at,
			published_at, last_error, created_at, updated_at
		from outbox_events
		where state in (?, ?) and next_attempt_at <= ?
		order by next_attempt_at, created_at
		limit ?`,
		StatePending,
		StateFailed,
		formatTime(dueAt.UTC()),
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list due outbox events: %w", err)
	}
	defer rows.Close()

	var records []OutboxRecord
	for rows.Next() {
		record, err := scanOutboxRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due outbox events: %w", err)
	}
	return records, nil
}

func (s *OutboxStore) MarkPublished(ctx context.Context, id string) error {
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `update outbox_events
		set state = ?, published_at = ?, last_error = null, updated_at = ?
		where id = ? and state != ?`,
		StatePublished,
		formatTime(now),
		formatTime(now),
		id,
		StatePublished,
	)
	if err != nil {
		return fmt.Errorf("mark outbox event published: %w", err)
	}
	return requireAffected(result)
}

func (s *OutboxStore) MarkFailed(ctx context.Context, id string, publishErr error, nextAttemptAt time.Time) error {
	if publishErr == nil {
		return fmt.Errorf("publish error is required")
	}
	if nextAttemptAt.IsZero() {
		return fmt.Errorf("next attempt time is required")
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `update outbox_events
		set state = ?,
			attempts = attempts + 1,
			next_attempt_at = ?,
			last_error = ?,
			updated_at = ?
		where id = ? and state != ?`,
		StateFailed,
		formatTime(nextAttemptAt.UTC()),
		publishErr.Error(),
		formatTime(now),
		id,
		StatePublished,
	)
	if err != nil {
		return fmt.Errorf("mark outbox event failed: %w", err)
	}
	return requireAffected(result)
}

type outboxScanner interface {
	Scan(dest ...any) error
}

func scanOutboxRecord(scanner outboxScanner) (OutboxRecord, error) {
	var record OutboxRecord
	var nextAttemptRaw string
	var publishedRaw sql.NullString
	var lastError sql.NullString
	var createdRaw string
	var updatedRaw string

	err := scanner.Scan(
		&record.ID,
		&record.Subject,
		&record.PayloadJSON,
		&record.State,
		&record.Attempts,
		&nextAttemptRaw,
		&publishedRaw,
		&lastError,
		&createdRaw,
		&updatedRaw,
	)
	if err != nil {
		return OutboxRecord{}, fmt.Errorf("scan outbox event: %w", err)
	}
	event, err := decodeJobEvent(record.PayloadJSON)
	if err != nil {
		return OutboxRecord{}, err
	}
	record.Payload = event

	nextAttemptAt, err := parseTime(nextAttemptRaw)
	if err != nil {
		return OutboxRecord{}, fmt.Errorf("parse next_attempt_at: %w", err)
	}
	createdAt, err := parseTime(createdRaw)
	if err != nil {
		return OutboxRecord{}, fmt.Errorf("parse created_at: %w", err)
	}
	updatedAt, err := parseTime(updatedRaw)
	if err != nil {
		return OutboxRecord{}, fmt.Errorf("parse updated_at: %w", err)
	}
	record.NextAttemptAt = nextAttemptAt
	record.CreatedAt = createdAt
	record.UpdatedAt = updatedAt

	if publishedRaw.Valid {
		publishedAt, err := parseTime(publishedRaw.String)
		if err != nil {
			return OutboxRecord{}, fmt.Errorf("parse published_at: %w", err)
		}
		record.PublishedAt = &publishedAt
	}
	if lastError.Valid {
		record.LastError = &lastError.String
	}
	return record, nil
}

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows: %w", err)
	}
	if affected != 1 {
		return ErrNotFound
	}
	return nil
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(raw string) (time.Time, error) {
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, err
	}
	return value.UTC(), nil
}
