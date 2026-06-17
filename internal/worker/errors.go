package worker

import "errors"

var (
	ErrInvalidJob    = errors.New("invalid job")
	ErrNotFound      = errors.New("job receipt not found")
	ErrJobDead       = errors.New("job is dead-lettered")
	ErrJobProcessing = errors.New("job is already processing")
	ErrStaleAttempt  = errors.New("job attempt is stale")
)
