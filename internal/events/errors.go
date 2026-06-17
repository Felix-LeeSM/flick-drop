package events

import "errors"

var (
	ErrInvalidEvent = errors.New("invalid event")
	ErrNotFound     = errors.New("outbox event not found")
)
