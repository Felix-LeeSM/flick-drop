package secrets

import "errors"

var (
	ErrInvalidInput     = errors.New("invalid secret input")
	ErrNotFound         = errors.New("secret not found")
	ErrConsumed         = errors.New("secret already consumed")
	ErrExpired          = errors.New("secret expired")
	ErrInvalidAccess    = errors.New("invalid secret access proof")
	ErrPayloadTooLarge  = errors.New("payload too large")
	ErrUnsupportedKind  = errors.New("unsupported secret kind")
	ErrUnsupportedViews = errors.New("unsupported max views")
)
