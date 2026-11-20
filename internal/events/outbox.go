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

// Excludes rows awaiting retry so the gauge reads the real backlog, not retry debt.
func (s *OutboxStore) CountPending(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `select count(*) from outbox_events where state = 'pending'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count pending outbox events: %w", err)
	}
	return n, nil
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
	// Capture now: the async publisher runs after this span has ended.
	injectTraceContext(ctx, &event)
	payloadJSON, err := event.JSON()
	if err != nil {
		return OutboxRecord{}, err
	}

	now := s.now().UTC()
	_, err = tx.ExecContext(ctx, `insert into outbox_events (
		id, subject, payload_json, state, attempts, next_attempt_at, created_at, updated_at
	) values ($1, $2, $3, $4, 0, $5, $6, $7)`,
		event.JobID,
		s.subject,
		string(payloadJSON),
		StatePending,
		now,
		now,
		now,
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

// FOR UPDATE SKIP LOCKED: replicas claim disjoint rows. The caller publishes while the
// locks are held, so a crash rolls the batch back to the due pool.
func (s *OutboxStore) ClaimDueTx(ctx context.Context, tx *sql.Tx, dueAt time.Time, limit int) ([]OutboxRecord, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive")
	}

	rows, err := tx.QueryContext(ctx, `select
			id, subject, payload_json, state, attempts, next_attempt_at,
			published_at, last_error, created_at, updated_at
		from outbox_events
		where state in ($1, $2) and next_attempt_at <= $3
		order by next_attempt_at, created_at
		limit $4
		for update skip locked`,
		StatePending,
		StateFailed,
		dueAt.UTC(),
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("claim due outbox events: %w", err)
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
		set state = $1, published_at = $2, last_error = null, updated_at = $3
		where id = $4 and state != $1`,
		StatePublished,
		now,
		now,
		id,
	)
	if err != nil {
		return fmt.Errorf("mark outbox event published: %w", err)
	}
	return requireAffected(result)
}

func markOutboxPublishedTx(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `update outbox_events
		set state = $1, published_at = $2, last_error = null, updated_at = $3
		where id = $4 and state != $1`,
		StatePublished,
		now,
		now,
		id,
	)
	if err != nil {
		return fmt.Errorf("mark outbox event published: %w", err)
	}
	return requireAffected(result)
}

func markOutboxFailedTx(ctx context.Context, tx *sql.Tx, id string, publishErr error, nextAttemptAt, now time.Time) error {
	if publishErr == nil {
		return fmt.Errorf("publish error is required")
	}
	if nextAttemptAt.IsZero() {
		return fmt.Errorf("next attempt time is required")
	}
	result, err := tx.ExecContext(ctx, `update outbox_events
		set state = $1,
			attempts = attempts + 1,
			next_attempt_at = $2,
			last_error = $3,
			updated_at = $4
		where id = $5 and state != $6`,
		StateFailed,
		nextAttemptAt.UTC(),
		publishErr.Error(),
		now,
		id,
		StatePublished,
	)
	if err != nil {
		return fmt.Errorf("mark outbox event failed: %w", err)
	}
	return requireAffected(result)
}

// Payloads name object keys and secret IDs; pruning them lets the API forget an opened secret.
const PublishedRetention = time.Hour

// Pending and failed rows stay: still due, or needed to diagnose a broken publish.
func PurgePublishedTx(ctx context.Context, tx *sql.Tx, now time.Time, limit int) error {
	cutoff := now.Add(-PublishedRetention)
	if _, err := tx.ExecContext(ctx, `delete from outbox_events where id in (
		select id from outbox_events
		where state = $1 and published_at <= $2
		order by published_at limit $3)`, StatePublished, cutoff, limit); err != nil {
		return fmt.Errorf("purge published outbox events: %w", err)
	}
	return nil
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
		set state = $1,
			attempts = attempts + 1,
			next_attempt_at = $2,
			last_error = $3,
			updated_at = $4
		where id = $5 and state != $6`,
		StateFailed,
		nextAttemptAt.UTC(),
		publishErr.Error(),
		now,
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
	var publishedAt sql.NullTime
	var lastError sql.NullString

	err := scanner.Scan(
		&record.ID,
		&record.Subject,
		&record.PayloadJSON,
		&record.State,
		&record.Attempts,
		&record.NextAttemptAt,
		&publishedAt,
		&lastError,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	if err != nil {
		return OutboxRecord{}, fmt.Errorf("scan outbox event: %w", err)
	}
	event, err := decodeJobEvent(record.PayloadJSON)
	if err != nil {
		return OutboxRecord{}, err
	}
	record.Payload = event

	if publishedAt.Valid {
		published := publishedAt.Time
		record.PublishedAt = &published
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
