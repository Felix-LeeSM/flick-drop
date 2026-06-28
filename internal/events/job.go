package events

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	KindDeleteSecret    = "delete_secret"
	KindDeleteOCIObject = "delete_oci_object"

	ReasonConsumed = "consumed"
	ReasonExpired  = "expired"
	ReasonOrphan   = "orphan"
	ReasonManual   = "manual"
	ReasonRetry    = "retry"
)

func NewJobID() (string, error) {
	var bytes [18]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}

type JobEvent struct {
	JobID       string    `json:"job_id"`
	Kind        string    `json:"kind"`
	SecretID    string    `json:"secret_id,omitempty"`
	ObjectKey   string    `json:"object_key,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	RequestedAt time.Time `json:"requested_at"`
	// TraceContext carries the W3C trace-context of the span that enqueued this
	// job, injected by the outbox at enqueue time so the worker can continue the
	// producer's trace across the async outbox -> NATS hop (#133). Telemetry IDs
	// only — never secret content. Absent when tracing is disabled.
	TraceContext map[string]string `json:"trace_context,omitempty"`
}

func (e JobEvent) Validate() error {
	if strings.TrimSpace(e.JobID) == "" {
		return fmt.Errorf("%w: job_id is required", ErrInvalidEvent)
	}
	if e.RequestedAt.IsZero() {
		return fmt.Errorf("%w: requested_at is required", ErrInvalidEvent)
	}

	switch e.Kind {
	case KindDeleteSecret:
		if strings.TrimSpace(e.SecretID) == "" {
			return fmt.Errorf("%w: secret_id is required for %s", ErrInvalidEvent, e.Kind)
		}
	case KindDeleteOCIObject:
		if strings.TrimSpace(e.ObjectKey) == "" {
			return fmt.Errorf("%w: object_key is required for %s", ErrInvalidEvent, e.Kind)
		}
	default:
		return fmt.Errorf("%w: unsupported kind %q", ErrInvalidEvent, e.Kind)
	}

	if e.Reason != "" && !validReason(e.Reason) {
		return fmt.Errorf("%w: unsupported reason %q", ErrInvalidEvent, e.Reason)
	}
	return nil
}

func (e JobEvent) JSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("marshal job event: %w", err)
	}
	return payload, nil
}

func DecodeJobEvent(raw []byte) (JobEvent, error) {
	return decodeJobEvent(string(raw))
}

func validReason(reason string) bool {
	switch reason {
	case ReasonConsumed, ReasonExpired, ReasonOrphan, ReasonManual, ReasonRetry:
		return true
	default:
		return false
	}
}

func decodeJobEvent(raw string) (JobEvent, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()

	var event JobEvent
	if err := decoder.Decode(&event); err != nil {
		return JobEvent{}, fmt.Errorf("%w: decode job event: %v", ErrInvalidEvent, err)
	}
	if err := event.Validate(); err != nil {
		return JobEvent{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return JobEvent{}, fmt.Errorf("%w: trailing job event JSON", ErrInvalidEvent)
	}
	return event, nil
}
