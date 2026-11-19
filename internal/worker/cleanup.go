package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
)

const defaultCleanupClientTimeout = 10 * time.Second

type CleanupRequest struct {
	SecretID string
	JobID    string
	Reason   string
}

type CleanupResponse struct {
	Cleaned bool
}

type CleanupAPI interface {
	CleanupSecret(context.Context, CleanupRequest) (CleanupResponse, error)
}

type CleanupClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type CleanupClientOptions struct {
	BaseURL       string
	InternalToken string
	HTTPClient    *http.Client
}

func NewCleanupClient(opts CleanupClientOptions) (*CleanupClient, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("internal api base url is required")
	}
	token := strings.TrimSpace(opts.InternalToken)
	if token == "" {
		return nil, fmt.Errorf("internal token is required")
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultCleanupClientTimeout}
	}
	return &CleanupClient{
		baseURL:    baseURL,
		token:      token,
		httpClient: httpClient,
	}, nil
}

func (c *CleanupClient) CleanupSecret(ctx context.Context, req CleanupRequest) (CleanupResponse, error) {
	if strings.TrimSpace(req.SecretID) == "" || strings.TrimSpace(req.JobID) == "" || strings.TrimSpace(req.Reason) == "" {
		return CleanupResponse{}, fmt.Errorf("%w: cleanup request is incomplete", ErrInvalidJob)
	}

	body, err := json.Marshal(struct {
		JobID  string `json:"job_id"`
		Reason string `json:"reason"`
	}{
		JobID:  req.JobID,
		Reason: req.Reason,
	})
	if err != nil {
		return CleanupResponse{}, fmt.Errorf("marshal cleanup request: %w", err)
	}

	endpoint := c.baseURL + "/internal/secrets/" + url.PathEscape(req.SecretID) + "/cleanup"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return CleanupResponse{}, fmt.Errorf("build cleanup request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Flick-Internal-Token", c.token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// *url.Error prints the request URL, which carries the secret ID; keep only op and cause.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return CleanupResponse{}, fmt.Errorf("call cleanup endpoint (%s): %w", urlErr.Op, urlErr.Err)
		}
		return CleanupResponse{}, fmt.Errorf("call cleanup endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, resp.Body)
		return CleanupResponse{}, fmt.Errorf("cleanup endpoint returned status %d", resp.StatusCode)
	}

	var decoded struct {
		ID      string `json:"id"`
		Cleaned bool   `json:"cleaned"`
	}
	decoder := json.NewDecoder(resp.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return CleanupResponse{}, fmt.Errorf("decode cleanup response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return CleanupResponse{}, fmt.Errorf("decode cleanup response: trailing JSON")
	}
	if decoded.ID != req.SecretID {
		// Neither ID goes into the text: worker errors land in logs and job tables.
		return CleanupResponse{}, fmt.Errorf("cleanup response named a different secret")
	}

	return CleanupResponse{Cleaned: decoded.Cleaned}, nil
}

func (c *CleanupClient) AcknowledgeObjectCleanup(ctx context.Context, event events.JobEvent) error {
	if event.Kind != events.KindDeleteOCIObject || event.JobID == "" || !requiresObjectAcknowledgement(event.ObjectKey) {
		return fmt.Errorf("%w: invalid object cleanup acknowledgement", ErrInvalidJob)
	}
	body, err := json.Marshal(struct {
		Backend   string `json:"backend,omitempty"`
		JobID     string `json:"job_id"`
		ObjectKey string `json:"object_key"`
	}{event.Backend, event.JobID, event.ObjectKey})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/object-reconciliation/ack", bytes.NewReader(body))
	if err != nil {
		return errors.New("build object cleanup acknowledgement failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flick-Internal-Token", c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.New("object cleanup acknowledgement request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("object cleanup acknowledgement returned status %d", resp.StatusCode)
	}
	return nil
}

type ObjectDeleter interface {
	Delete(ctx context.Context, key string) error
}

// An unknown backend fails the job permanently: a silent wrong-bucket delete leaks the real object.
type CleanupHandler struct {
	api      CleanupAPI
	backends map[string]ObjectDeleter
}

// Empty map = storage disabled: a managed-key delete fails (retried), any other stray delete is a no-op.
func NewCleanupHandler(api CleanupAPI, backends map[string]ObjectDeleter) (*CleanupHandler, error) {
	if api == nil {
		return nil, fmt.Errorf("cleanup api is required")
	}
	return &CleanupHandler{api: api, backends: backends}, nil
}

func (h *CleanupHandler) HandleJob(ctx context.Context, event events.JobEvent) error {
	if event.Kind == events.KindDeleteOCIObject {
		return h.handleObjectDelete(ctx, event)
	}
	reason, err := cleanupReason(event)
	if err != nil {
		return err
	}
	if strings.TrimSpace(event.SecretID) == "" {
		return fmt.Errorf("%w: secret_id is required", ErrInvalidJob)
	}
	if strings.TrimSpace(event.JobID) == "" {
		return fmt.Errorf("%w: job_id is required", ErrInvalidJob)
	}

	_, err = h.api.CleanupSecret(ctx, CleanupRequest{
		SecretID: event.SecretID,
		JobID:    event.JobID,
		Reason:   reason,
	})
	if err != nil {
		return err
	}
	return nil
}

// Deletes directly, not via the API, so object and DB cleanup retry independently. Idempotent.
func (h *CleanupHandler) handleObjectDelete(ctx context.Context, event events.JobEvent) error {
	if strings.TrimSpace(event.ObjectKey) == "" {
		return fmt.Errorf("%w: object_key is required", ErrInvalidJob)
	}
	if len(h.backends) == 0 {
		if requiresObjectAcknowledgement(event.ObjectKey) {
			return errors.New("managed object deletion requires object storage")
		}
		return nil
	}
	objects, ok := h.backends[event.Backend]
	if !ok {
		return fmt.Errorf("%w: object delete names unconfigured backend %q", ErrInvalidJob, event.Backend)
	}
	if err := objects.Delete(ctx, event.ObjectKey); err != nil {
		return objectDeleteError{event: event, err: err}
	}
	return nil
}

// The key carries a secret ID and lands in never-pruned job tables and logs, so the key and
// its URL-escaped forms are masked out of the SDK message too.
type objectDeleteError struct {
	event events.JobEvent
	err   error
}

func (e objectDeleteError) Error() string {
	cause := e.err.Error()
	for _, form := range []string{e.event.ObjectKey, url.PathEscape(e.event.ObjectKey), url.QueryEscape(e.event.ObjectKey)} {
		cause = strings.ReplaceAll(cause, form, "<object>")
	}
	return fmt.Sprintf("delete object for job %s (reason %s, backend %s): %s", e.event.JobID, e.event.Reason, e.event.Backend, cause)
}

func (e objectDeleteError) Unwrap() error { return e.err }

func cleanupReason(event events.JobEvent) (string, error) {
	switch event.Kind {
	case events.KindDeleteSecret:
		if event.Reason != "" {
			if !validCleanupReason(event.Reason) {
				return "", fmt.Errorf("%w: unsupported cleanup reason %q", ErrInvalidJob, event.Reason)
			}
			return event.Reason, nil
		}
		return events.ReasonConsumed, nil
	default:
		return "", fmt.Errorf("%w: unsupported cleanup job kind %q", ErrInvalidJob, event.Kind)
	}
}

func validCleanupReason(reason string) bool {
	switch reason {
	case events.ReasonConsumed, events.ReasonExpired, events.ReasonOrphan, events.ReasonManual, events.ReasonRetry:
		return true
	default:
		return false
	}
}

var _ JobHandler = (*CleanupHandler)(nil)
var _ CleanupAPI = (*CleanupClient)(nil)

func requiresObjectAcknowledgement(key string) bool {
	for _, prefix := range []string{"managed/secrets/", "managed/requests/"} {
		if strings.HasPrefix(key, prefix) && key != prefix {
			return true
		}
	}
	return false
}
