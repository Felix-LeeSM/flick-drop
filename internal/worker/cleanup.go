package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Felix-LeeSM/burn-links/internal/events"
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
	httpReq.Header.Set("X-BurnLink-Internal-Token", c.token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
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
		return CleanupResponse{}, fmt.Errorf("cleanup response id = %q, want %q", decoded.ID, req.SecretID)
	}

	return CleanupResponse{Cleaned: decoded.Cleaned}, nil
}

type CleanupHandler struct {
	api CleanupAPI
}

func NewCleanupHandler(api CleanupAPI) (*CleanupHandler, error) {
	if api == nil {
		return nil, fmt.Errorf("cleanup api is required")
	}
	return &CleanupHandler{api: api}, nil
}

func (h *CleanupHandler) HandleJob(ctx context.Context, event events.JobEvent) error {
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
	case events.KindExpireSecret:
		if event.Reason != "" {
			if !validCleanupReason(event.Reason) {
				return "", fmt.Errorf("%w: unsupported cleanup reason %q", ErrInvalidJob, event.Reason)
			}
			return event.Reason, nil
		}
		return events.ReasonExpired, nil
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
