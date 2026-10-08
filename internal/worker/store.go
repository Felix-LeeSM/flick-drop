package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	// ProcessingLease outlasts JobTimeout so a live handler has time to stop
	// before another delivery reclaims its receipt. No process-local ownership:
	// rolling deployments can briefly share the same worker database.
	ProcessingLease = time.Minute

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

func (s *ReceiptStore) Start(ctx context.Context, jobID, kind, payloadJSON string, maxFailures int) (StartResult, error) {
	if jobID == "" || kind == "" || payloadJSON == "" || maxFailures < 1 {
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
	now := s.now().UTC()
	if !created && receipt.State == StateProcessing {
		if now.Before(receipt.UpdatedAt.Add(ProcessingLease)) {
			return StartResult{}, ErrJobProcessing
		}
		// A lease expiry says nothing about the side effect. Retain the attempt
		// as interrupted (failed with no error), then replay the idempotent job.
		// Interruptions do not spend the handler's failure budget.
		if _, err := tx.ExecContext(ctx, `update job_attempts
			set result = ?, finished_at = ?, error = null
			where job_id = ? and result = ?`,
			AttemptFailed, formatTime(now), jobID, AttemptRunning); err != nil {
			return StartResult{}, fmt.Errorf("recover interrupted attempt: %w", err)
		}
	}

	if !created {
		failures, err := failureCount(ctx, tx, jobID)
		if err != nil {
			return StartResult{}, err
		}
		if failures >= maxFailures {
			// Older workers could commit the final failure before its dead letter.
			// Finish that transition without claiming another handler attempt.
			var lastError string
			if err := tx.QueryRowContext(ctx, `select error from job_attempts
				where job_id = ? and result = ? and error is not null
				order by attempt desc limit 1`, jobID, AttemptFailed).Scan(&lastError); err != nil {
				return StartResult{}, fmt.Errorf("load final job failure: %w", err)
			}
			if err := deadLetter(ctx, tx, receipt, payloadJSON, lastError, now); err != nil {
				return StartResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return StartResult{}, fmt.Errorf("commit recovered dead letter: %w", err)
			}
			return StartResult{}, ErrJobDead
		}
	}

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
	_, err := s.finishAttempt(ctx, attemptID, AttemptSucceeded, nil, "", 0)
	return err
}

// MarkFailed commits the failure and, at the limit, its terminal receipt and
// dead letter together. The returned bool is true only after that commit.
func (s *ReceiptStore) MarkFailed(ctx context.Context, attemptID int64, jobErr error, payloadJSON string, maxFailures int) (bool, error) {
	if jobErr == nil {
		return false, fmt.Errorf("job error is required")
	}
	if payloadJSON == "" || maxFailures < 1 {
		return false, ErrInvalidJob
	}
	return s.finishAttempt(ctx, attemptID, AttemptFailed, jobErr, payloadJSON, maxFailures)
}

func failureCount(ctx context.Context, q receiptQueryer, jobID string) (int, error) {
	var count int
	err := q.QueryRowContext(ctx, `select count(*) from job_attempts
		where job_id = ? and result = ? and error is not null`, jobID, AttemptFailed).Scan(&count)
	return count, err
}

func deadLetter(ctx context.Context, tx *sql.Tx, receipt Receipt, payloadJSON, jobError string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `update job_receipts
		set state = ?, last_error = ?, updated_at = ?, completed_at = ?
		where job_id = ? and state = ? and attempts = ?`,
		StateDead, jobError, formatTime(now), formatTime(now),
		receipt.JobID, receipt.State, receipt.Attempts)
	if err != nil {
		return fmt.Errorf("update dead job receipt: %w", err)
	}
	if err := requireAffected(result); err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `insert into dead_letters (
		job_id, kind, payload_json, error, created_at
	) values (?, ?, ?, ?, ?)
	on conflict(job_id) do update set
		kind = excluded.kind,
		payload_json = excluded.payload_json,
		error = excluded.error,
		created_at = excluded.created_at`,
		receipt.JobID,
		receipt.Kind,
		payloadJSON,
		jobError,
		formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("upsert dead letter: %w", err)
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

func (s *ReceiptStore) finishAttempt(ctx context.Context, attemptID int64, result string, jobErr error, payloadJSON string, maxFailures int) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin finish job attempt: %w", err)
	}
	defer rollback(tx)

	attempt, err := loadAttempt(ctx, tx, attemptID)
	if err != nil {
		return false, err
	}
	if attempt.Result != AttemptRunning {
		return false, ErrStaleAttempt
	}
	receipt, err := loadReceipt(ctx, tx, attempt.JobID)
	if err != nil {
		return false, err
	}
	if receipt.State != StateProcessing || receipt.Attempts != attempt.Attempt {
		return false, ErrStaleAttempt
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
		return false, fmt.Errorf("update job attempt: %w", err)
	}
	if err := requireAffected(updateResult); err != nil {
		return false, err
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
		return false, fmt.Errorf("update job receipt: %w", err)
	}
	affected, err := updateResult.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read affected rows: %w", err)
	}
	if affected != 1 {
		return false, ErrStaleAttempt
	}

	dead := false
	if result == AttemptFailed {
		failures, err := failureCount(ctx, tx, attempt.JobID)
		if err != nil {
			return false, err
		}
		if failures >= maxFailures {
			receipt.State = StateFailed
			if err := deadLetter(ctx, tx, receipt, payloadJSON, *errText, now); err != nil {
				return false, err
			}
			dead = true
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit finish job attempt: %w", err)
	}
	return dead, nil
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
