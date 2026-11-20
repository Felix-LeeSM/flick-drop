package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	// Outlasts JobTimeout so a live handler can stop before another delivery reclaims it;
	// rolling deployments can briefly share one worker database.
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

func leaseInterval() string {
	return fmt.Sprintf("%d seconds", int(ProcessingLease/time.Second))
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

	now := s.now().UTC()
	// One conditional upsert claims atomically: under READ COMMITTED, Postgres re-evaluates the
	// WHERE against the locked row (EvalPlanQual), so two starts never both pass a stale check.
	var attempts int
	err = tx.QueryRowContext(ctx, `insert into job_receipts (
		job_id, kind, state, attempts, first_seen_at, updated_at
	) values ($1, $2, $3, 1, $4, $4)
	on conflict (job_id) do update set
		state = $3,
		attempts = job_receipts.attempts + 1,
		updated_at = $4,
		last_error = null
	where job_receipts.kind = $2
		and job_receipts.state <> $5
		and job_receipts.state <> $6
		and (job_receipts.state <> $3 or job_receipts.updated_at + $7::interval <= $4)
		and (
			select count(*) from job_attempts prior
			where prior.job_id = job_receipts.job_id
				and prior.result = $8
				and prior.error is not null
		) < $9
	returning attempts`,
		jobID,
		kind,
		StateProcessing,
		now,
		StateSucceeded,
		StateDead,
		leaseInterval(),
		AttemptFailed,
		maxFailures,
	).Scan(&attempts)

	if errors.Is(err, sql.ErrNoRows) {
		// Classify the refusal: kind, terminal states, live lease, then over budget.
		receipt, loadErr := loadReceipt(ctx, tx, jobID)
		if loadErr != nil {
			return StartResult{}, loadErr
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
		if receipt.State == StateProcessing && now.Before(receipt.UpdatedAt.Add(ProcessingLease)) {
			return StartResult{}, ErrJobProcessing
		}
		// Over budget: finish the dead-letter transition without claiming another attempt.
		if _, err := tx.ExecContext(ctx, `update job_attempts
			set result = $1, finished_at = $2, error = null
			where job_id = $3 and result = $4`,
			AttemptFailed, now, jobID, AttemptRunning); err != nil {
			return StartResult{}, fmt.Errorf("recover interrupted attempt: %w", err)
		}
		failures, countErr := failureCount(ctx, tx, jobID)
		if countErr != nil {
			return StartResult{}, countErr
		}
		if failures >= maxFailures {
			// Older workers could commit the final failure before its dead letter.
			var lastError string
			if err := tx.QueryRowContext(ctx, `select error from job_attempts
				where job_id = $1 and result = $2 and error is not null
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
		return StartResult{}, ErrJobProcessing
	}
	if err != nil {
		return StartResult{}, fmt.Errorf("claim job lease: %w", err)
	}

	// A lease expiry says nothing about the side effect: mark any running attempt interrupted
	// (not spending failure budget), then replay the idempotent job.
	if _, err := tx.ExecContext(ctx, `update job_attempts
		set result = $1, finished_at = $2, error = null
		where job_id = $3 and result = $4`,
		AttemptFailed, now, jobID, AttemptRunning); err != nil {
		return StartResult{}, fmt.Errorf("recover interrupted attempt: %w", err)
	}

	attemptNumber := attempts
	var attemptID int64
	if err := tx.QueryRowContext(ctx, `insert into job_attempts (
		job_id, attempt, started_at, result
	) values ($1, $2, $3, $4)
	returning id`,
		jobID,
		attemptNumber,
		now,
		AttemptRunning,
	).Scan(&attemptID); err != nil {
		return StartResult{}, fmt.Errorf("insert job attempt: %w", err)
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

// Commits the failure and, at the limit, its terminal receipt and dead letter together.
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
		where job_id = $1 and result = $2 and error is not null`, jobID, AttemptFailed).Scan(&count)
	return count, err
}

func deadLetter(ctx context.Context, tx *sql.Tx, receipt Receipt, payloadJSON, jobError string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `update job_receipts
		set state = $1, last_error = $2, updated_at = $3, completed_at = $4
		where job_id = $5 and state = $6 and attempts = $7`,
		StateDead, jobError, now, now,
		receipt.JobID, receipt.State, receipt.Attempts)
	if err != nil {
		return fmt.Errorf("update dead job receipt: %w", err)
	}
	if err := requireAffected(result); err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `insert into dead_letters (
		job_id, kind, payload_json, error, created_at
	) values ($1, $2, $3, $4, $5)
	on conflict(job_id) do update set
		kind = excluded.kind,
		payload_json = excluded.payload_json,
		error = excluded.error,
		created_at = excluded.created_at`,
		receipt.JobID,
		receipt.Kind,
		payloadJSON,
		jobError,
		now,
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
		where job_id = $1
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
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx, `select job_id, kind, payload_json, error, created_at
		from dead_letters
		where job_id = $1`, jobID).Scan(
		&record.JobID,
		&record.Kind,
		&record.PayloadJSON,
		&record.Error,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DeadLetter{}, ErrNotFound
	}
	if err != nil {
		return DeadLetter{}, fmt.Errorf("load dead letter: %w", err)
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
		set result = $1, finished_at = $2, error = $3
		where id = $4 and result = $5`,
		result,
		now,
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
	var completedAt *time.Time
	if result == AttemptFailed {
		receiptState = StateFailed
	} else {
		completed := now
		completedAt = &completed
	}

	updateResult, err = tx.ExecContext(ctx, `update job_receipts
		set state = $1, last_error = $2, updated_at = $3, completed_at = coalesce($4, completed_at)
		where job_id = $5 and state = $6 and attempts = $7`,
		receiptState,
		errText,
		now,
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
	var completedAt sql.NullTime
	err := q.QueryRowContext(ctx, `select
			job_id, kind, state, attempts, last_error, first_seen_at, updated_at, completed_at
		from job_receipts
		where job_id = $1`, jobID).Scan(
		&receipt.JobID,
		&receipt.Kind,
		&receipt.State,
		&receipt.Attempts,
		&lastError,
		&receipt.FirstSeenAt,
		&receipt.UpdatedAt,
		&completedAt,
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
	if completedAt.Valid {
		completed := completedAt.Time
		receipt.CompletedAt = &completed
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
		where id = $1`, id)
	return scanAttempt(row)
}

type attemptScanner interface {
	Scan(dest ...any) error
}

func scanAttempt(scanner attemptScanner) (Attempt, error) {
	var attempt Attempt
	var finishedAt sql.NullTime
	var errText sql.NullString
	err := scanner.Scan(
		&attempt.ID,
		&attempt.JobID,
		&attempt.Attempt,
		&attempt.StartedAt,
		&finishedAt,
		&attempt.Result,
		&errText,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, ErrNotFound
	}
	if err != nil {
		return Attempt{}, fmt.Errorf("scan job attempt: %w", err)
	}
	if finishedAt.Valid {
		finished := finishedAt.Time
		attempt.FinishedAt = &finished
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
