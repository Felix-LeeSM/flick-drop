package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	StateProcessing = "processing"
	StateSucceeded  = "succeeded"
	StateFailed     = "failed"
	StateDead       = "dead"

	AttemptRunning   = "running"
	AttemptSucceeded = "succeeded"
	AttemptFailed    = "failed"
)

type ReceiptStore struct {
	db  *sql.DB
	now func() time.Time
}

type Receipt struct {
	JobID       string
	Kind        string
	State       string
	Attempts    int
	LastError   *string
	FirstSeenAt time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
}

type Attempt struct {
	ID         int64
	JobID      string
	Attempt    int
	StartedAt  time.Time
	FinishedAt *time.Time
	Result     string
	Error      *string
}

type StartResult struct {
	Attempt          Attempt
	AlreadySucceeded bool
}

type DeadLetter struct {
	JobID       string
	Kind        string
	PayloadJSON string
	Error       string
	CreatedAt   time.Time
}

func NewReceiptStore(db *sql.DB) (*ReceiptStore, error) {
	if db == nil {
		return nil, fmt.Errorf("db is required")
	}
	return &ReceiptStore{
		db:  db,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *ReceiptStore) SetNowForTest(now func() time.Time) {
	s.now = now
}

func (s *ReceiptStore) Start(ctx context.Context, jobID, kind string) (StartResult, error) {
	if jobID == "" || kind == "" {
		return StartResult{}, ErrInvalidJob
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StartResult{}, fmt.Errorf("begin start job: %w", err)
	}
	defer rollback(tx)

	created := false
	receipt, err := loadReceipt(ctx, tx, jobID)
	if errors.Is(err, ErrNotFound) {
		created = true
		now := s.now().UTC()
		receipt = Receipt{
			JobID:       jobID,
			Kind:        kind,
			State:       StateProcessing,
			Attempts:    0,
			FirstSeenAt: now,
			UpdatedAt:   now,
		}
		if _, err := tx.ExecContext(ctx, `insert into job_receipts (
			job_id, kind, state, attempts, first_seen_at, updated_at
		) values (?, ?, ?, 0, ?, ?)`,
			receipt.JobID,
			receipt.Kind,
			receipt.State,
			formatTime(receipt.FirstSeenAt),
			formatTime(receipt.UpdatedAt),
		); err != nil {
			return StartResult{}, fmt.Errorf("insert job receipt: %w", err)
		}
	} else if err != nil {
		return StartResult{}, err
	}

	if receipt.Kind != kind {
		return StartResult{}, ErrInvalidJob
	}
	if receipt.State == StateSucceeded {
		if err := tx.Commit(); err != nil {
			return StartResult{}, fmt.Errorf("commit duplicate succeeded job: %w", err)
		}
		return StartResult{AlreadySucceeded: true}, nil
	}
	if receipt.State == StateDead {
		return StartResult{}, ErrJobDead
	}
	if !created && receipt.State == StateProcessing {
		return StartResult{}, ErrJobProcessing
	}

	now := s.now().UTC()
	attemptNumber := receipt.Attempts + 1
	result, err := tx.ExecContext(ctx, `update job_receipts
		set state = ?, attempts = ?, updated_at = ?, last_error = null
		where job_id = ?`,
		StateProcessing,
		attemptNumber,
		formatTime(now),
		jobID,
	)
	if err != nil {
		return StartResult{}, fmt.Errorf("update job receipt for attempt: %w", err)
	}
	if err := requireAffected(result); err != nil {
		return StartResult{}, err
	}

	result, err = tx.ExecContext(ctx, `insert into job_attempts (
		job_id, attempt, started_at, result
	) values (?, ?, ?, ?)`,
		jobID,
		attemptNumber,
		formatTime(now),
		AttemptRunning,
	)
	if err != nil {
		return StartResult{}, fmt.Errorf("insert job attempt: %w", err)
	}
	attemptID, err := result.LastInsertId()
	if err != nil {
		return StartResult{}, fmt.Errorf("read attempt id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return StartResult{}, fmt.Errorf("commit start job: %w", err)
	}

	return StartResult{
		Attempt: Attempt{
			ID:        attemptID,
			JobID:     jobID,
			Attempt:   attemptNumber,
			StartedAt: now,
			Result:    AttemptRunning,
		},
	}, nil
}

func (s *ReceiptStore) MarkSucceeded(ctx context.Context, attemptID int64) error {
	return s.finishAttempt(ctx, attemptID, AttemptSucceeded, nil)
}

func (s *ReceiptStore) MarkFailed(ctx context.Context, attemptID int64, jobErr error) error {
	if jobErr == nil {
		return fmt.Errorf("job error is required")
	}
	return s.finishAttempt(ctx, attemptID, AttemptFailed, jobErr)
}

func (s *ReceiptStore) DeadLetter(ctx context.Context, jobID, kind, payloadJSON string, jobErr error) error {
	if jobID == "" || kind == "" || payloadJSON == "" {
		return ErrInvalidJob
	}
	if jobErr == nil {
		return fmt.Errorf("job error is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin dead letter job: %w", err)
	}
	defer rollback(tx)

	receipt, err := loadReceipt(ctx, tx, jobID)
	if err == nil {
		if receipt.Kind != kind || receipt.State == StateSucceeded {
			return ErrInvalidJob
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	now := s.now().UTC()
	_, err = tx.ExecContext(ctx, `insert into job_receipts (
		job_id, kind, state, attempts, last_error, first_seen_at, updated_at, completed_at
	) values (?, ?, ?, 0, ?, ?, ?, ?)
	on conflict(job_id) do update set
		state = excluded.state,
		last_error = excluded.last_error,
		updated_at = excluded.updated_at,
		completed_at = excluded.completed_at`,
		jobID,
		kind,
		StateDead,
		jobErr.Error(),
		formatTime(now),
		formatTime(now),
		formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("upsert dead job receipt: %w", err)
	}

	_, err = tx.ExecContext(ctx, `insert into dead_letters (
		job_id, kind, payload_json, error, created_at
	) values (?, ?, ?, ?, ?)
	on conflict(job_id) do update set
		kind = excluded.kind,
		payload_json = excluded.payload_json,
		error = excluded.error,
		created_at = excluded.created_at`,
		jobID,
		kind,
		payloadJSON,
		jobErr.Error(),
		formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("upsert dead letter: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit dead letter job: %w", err)
	}
	return nil
}

func (s *ReceiptStore) Receipt(ctx context.Context, jobID string) (Receipt, error) {
	return loadReceipt(ctx, s.db, jobID)
}

func (s *ReceiptStore) Attempts(ctx context.Context, jobID string) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `select
			id, job_id, attempt, started_at, finished_at, result, error
		from job_attempts
		where job_id = ?
		order by attempt`, jobID)
	if err != nil {
		return nil, fmt.Errorf("list job attempts: %w", err)
	}
	defer rows.Close()

	var attempts []Attempt
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job attempts: %w", err)
	}
	return attempts, nil
}

func (s *ReceiptStore) DeadLetterRecord(ctx context.Context, jobID string) (DeadLetter, error) {
	var record DeadLetter
	var createdRaw string
	err := s.db.QueryRowContext(ctx, `select job_id, kind, payload_json, error, created_at
		from dead_letters
		where job_id = ?`, jobID).Scan(
		&record.JobID,
		&record.Kind,
		&record.PayloadJSON,
		&record.Error,
		&createdRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DeadLetter{}, ErrNotFound
	}
	if err != nil {
		return DeadLetter{}, fmt.Errorf("load dead letter: %w", err)
	}
	createdAt, err := parseTime(createdRaw)
	if err != nil {
		return DeadLetter{}, fmt.Errorf("parse dead letter created_at: %w", err)
	}
	record.CreatedAt = createdAt
	return record, nil
}

func (s *ReceiptStore) finishAttempt(ctx context.Context, attemptID int64, result string, jobErr error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finish job attempt: %w", err)
	}
	defer rollback(tx)

	attempt, err := loadAttempt(ctx, tx, attemptID)
	if err != nil {
		return err
	}
	if attempt.Result != AttemptRunning {
		return fmt.Errorf("job attempt is already finished")
	}
	receipt, err := loadReceipt(ctx, tx, attempt.JobID)
	if err != nil {
		return err
	}
	if receipt.State != StateProcessing || receipt.Attempts != attempt.Attempt {
		return ErrStaleAttempt
	}

	now := s.now().UTC()
	var errText *string
	if jobErr != nil {
		text := jobErr.Error()
		errText = &text
	}
	updateResult, err := tx.ExecContext(ctx, `update job_attempts
		set result = ?, finished_at = ?, error = ?
		where id = ? and result = ?`,
		result,
		formatTime(now),
		errText,
		attemptID,
		AttemptRunning,
	)
	if err != nil {
		return fmt.Errorf("update job attempt: %w", err)
	}
	if err := requireAffected(updateResult); err != nil {
		return err
	}

	receiptState := StateSucceeded
	var completedAt *string
	if result == AttemptFailed {
		receiptState = StateFailed
	} else {
		completed := formatTime(now)
		completedAt = &completed
	}

	updateResult, err = tx.ExecContext(ctx, `update job_receipts
		set state = ?, last_error = ?, updated_at = ?, completed_at = coalesce(?, completed_at)
		where job_id = ? and state = ? and attempts = ?`,
		receiptState,
		errText,
		formatTime(now),
		completedAt,
		attempt.JobID,
		StateProcessing,
		attempt.Attempt,
	)
	if err != nil {
		return fmt.Errorf("update job receipt: %w", err)
	}
	affected, err := updateResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows: %w", err)
	}
	if affected != 1 {
		return ErrStaleAttempt
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit finish job attempt: %w", err)
	}
	return nil
}

type receiptQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadReceipt(ctx context.Context, q receiptQueryer, jobID string) (Receipt, error) {
	var receipt Receipt
	var lastError sql.NullString
	var firstSeenRaw string
	var updatedRaw string
	var completedRaw sql.NullString
	err := q.QueryRowContext(ctx, `select
			job_id, kind, state, attempts, last_error, first_seen_at, updated_at, completed_at
		from job_receipts
		where job_id = ?`, jobID).Scan(
		&receipt.JobID,
		&receipt.Kind,
		&receipt.State,
		&receipt.Attempts,
		&lastError,
		&firstSeenRaw,
		&updatedRaw,
		&completedRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, ErrNotFound
	}
	if err != nil {
		return Receipt{}, fmt.Errorf("load job receipt: %w", err)
	}
	if lastError.Valid {
		receipt.LastError = &lastError.String
	}
	firstSeenAt, err := parseTime(firstSeenRaw)
	if err != nil {
		return Receipt{}, fmt.Errorf("parse first_seen_at: %w", err)
	}
	updatedAt, err := parseTime(updatedRaw)
	if err != nil {
		return Receipt{}, fmt.Errorf("parse updated_at: %w", err)
	}
	receipt.FirstSeenAt = firstSeenAt
	receipt.UpdatedAt = updatedAt
	if completedRaw.Valid {
		completedAt, err := parseTime(completedRaw.String)
		if err != nil {
			return Receipt{}, fmt.Errorf("parse completed_at: %w", err)
		}
		receipt.CompletedAt = &completedAt
	}
	return receipt, nil
}

type attemptQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadAttempt(ctx context.Context, q attemptQueryer, id int64) (Attempt, error) {
	row := q.QueryRowContext(ctx, `select
		id, job_id, attempt, started_at, finished_at, result, error
		from job_attempts
		where id = ?`, id)
	return scanAttempt(row)
}

type attemptScanner interface {
	Scan(dest ...any) error
}

func scanAttempt(scanner attemptScanner) (Attempt, error) {
	var attempt Attempt
	var startedRaw string
	var finishedRaw sql.NullString
	var errText sql.NullString
	err := scanner.Scan(
		&attempt.ID,
		&attempt.JobID,
		&attempt.Attempt,
		&startedRaw,
		&finishedRaw,
		&attempt.Result,
		&errText,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, ErrNotFound
	}
	if err != nil {
		return Attempt{}, fmt.Errorf("scan job attempt: %w", err)
	}
	startedAt, err := parseTime(startedRaw)
	if err != nil {
		return Attempt{}, fmt.Errorf("parse started_at: %w", err)
	}
	attempt.StartedAt = startedAt
	if finishedRaw.Valid {
		finishedAt, err := parseTime(finishedRaw.String)
		if err != nil {
			return Attempt{}, fmt.Errorf("parse finished_at: %w", err)
		}
		attempt.FinishedAt = &finishedAt
	}
	if errText.Valid {
		attempt.Error = &errText.String
	}
	return attempt, nil
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
