package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
)

func TestCleanupClientSendsInternalCleanupRequest(t *testing.T) {
	var gotPath string
	var gotToken string
	var gotBody map[string]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotToken = r.Header.Get("X-Flick-Internal-Token")
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if strings.Contains(strings.Join(mapValues(gotBody), " "), "passphrase") {
			t.Fatal("cleanup request body included sensitive text")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sec/1","cleaned":true}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewCleanupClient(CleanupClientOptions{
		BaseURL:       server.URL,
		InternalToken: "test-token",
		HTTPClient:    server.Client(),
	})
	if err != nil {
		t.Fatalf("new cleanup client: %v", err)
	}

	result, err := client.CleanupSecret(context.Background(), CleanupRequest{
		SecretID: "sec/1",
		JobID:    "job_1",
		Reason:   events.ReasonExpired,
	})
	if err != nil {
		t.Fatalf("cleanup secret: %v", err)
	}
	if !result.Cleaned {
		t.Fatal("cleaned = false, want true")
	}
	if gotPath != "/internal/secrets/sec%2F1/cleanup" {
		t.Fatalf("path = %q, want escaped cleanup path", gotPath)
	}
	if gotToken != "test-token" {
		t.Fatalf("internal token = %q, want test-token", gotToken)
	}
	if gotBody["job_id"] != "job_1" || gotBody["reason"] != events.ReasonExpired {
		t.Fatalf("request body = %#v, want cleanup metadata", gotBody)
	}
	if _, ok := gotBody["secret_id"]; ok {
		t.Fatal("request body must not include secret_id")
	}
}

func TestCleanupClientTreatsAlreadyCleanedResponseAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sec_1","cleaned":false}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewCleanupClient(CleanupClientOptions{
		BaseURL:       server.URL,
		InternalToken: "test-token",
		HTTPClient:    server.Client(),
	})
	if err != nil {
		t.Fatalf("new cleanup client: %v", err)
	}

	result, err := client.CleanupSecret(context.Background(), CleanupRequest{
		SecretID: "sec_1",
		JobID:    "job_1",
		Reason:   events.ReasonRetry,
	})
	if err != nil {
		t.Fatalf("cleanup secret: %v", err)
	}
	if result.Cleaned {
		t.Fatal("cleaned = true, want false")
	}
}

func TestCleanupClientReturnsErrorForRetryableFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"try later"}}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewCleanupClient(CleanupClientOptions{
		BaseURL:       server.URL,
		InternalToken: "test-token",
		HTTPClient:    server.Client(),
	})
	if err != nil {
		t.Fatalf("new cleanup client: %v", err)
	}

	_, err = client.CleanupSecret(context.Background(), CleanupRequest{
		SecretID: "sec_1",
		JobID:    "job_1",
		Reason:   events.ReasonExpired,
	})
	if err == nil {
		t.Fatal("cleanup error = nil, want error")
	}
}

func TestCleanupClientReturnsErrorForStalledAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"id":"sec_1","cleaned":true}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewCleanupClient(CleanupClientOptions{
		BaseURL:       server.URL,
		InternalToken: "test-token",
		HTTPClient:    &http.Client{Timeout: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("new cleanup client: %v", err)
	}

	_, err = client.CleanupSecret(context.Background(), CleanupRequest{
		SecretID: "sec_1",
		JobID:    "job_1",
		Reason:   events.ReasonExpired,
	})
	if err == nil {
		t.Fatal("cleanup error = nil, want stalled api timeout")
	}
}

func TestCleanupClientRequiresConfiguration(t *testing.T) {
	if _, err := NewCleanupClient(CleanupClientOptions{InternalToken: "test-token"}); err == nil {
		t.Fatal("missing base url error = nil, want error")
	}
	if _, err := NewCleanupClient(CleanupClientOptions{BaseURL: "http://internal-api"}); err == nil {
		t.Fatal("missing internal token error = nil, want error")
	}
}

func TestCleanupHandlerCallsClientForSecretCleanupKinds(t *testing.T) {
	api := &fakeCleanupAPI{}
	handler, err := NewCleanupHandler(api, nil)
	if err != nil {
		t.Fatalf("new cleanup handler: %v", err)
	}

	if err := handler.HandleJob(context.Background(), cleanupEvent(events.KindExpireSecret, "")); err != nil {
		t.Fatalf("handle expire cleanup: %v", err)
	}
	if err := handler.HandleJob(context.Background(), cleanupEvent(events.KindDeleteSecret, events.ReasonManual)); err != nil {
		t.Fatalf("handle delete cleanup: %v", err)
	}

	if len(api.calls) != 2 {
		t.Fatalf("cleanup calls = %d, want 2", len(api.calls))
	}
	if api.calls[0].Reason != events.ReasonExpired {
		t.Fatalf("expire reason = %q, want expired", api.calls[0].Reason)
	}
	if api.calls[1].Reason != events.ReasonManual {
		t.Fatalf("delete reason = %q, want manual", api.calls[1].Reason)
	}
}

func TestCleanupHandlerRejectsUnsupportedJobKind(t *testing.T) {
	api := &fakeCleanupAPI{}
	handler, err := NewCleanupHandler(api, nil)
	if err != nil {
		t.Fatalf("new cleanup handler: %v", err)
	}

	err = handler.HandleJob(context.Background(), events.JobEvent{
		JobID:       "job_unknown",
		Kind:        "totally_unknown_kind",
		RequestedAt: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("handle unsupported kind error = %v, want ErrInvalidJob", err)
	}
	if len(api.calls) != 0 {
		t.Fatalf("cleanup calls = %d, want 0", len(api.calls))
	}
}

type fakeObjectDeleter struct {
	deleted []string
	err     error
}

func (f *fakeObjectDeleter) Delete(_ context.Context, key string) error {
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, key)
	return nil
}

func TestCleanupHandlerDeletesObject(t *testing.T) {
	api := &fakeCleanupAPI{}
	obj := &fakeObjectDeleter{}
	handler, err := NewCleanupHandler(api, obj)
	if err != nil {
		t.Fatalf("new cleanup handler: %v", err)
	}

	err = handler.HandleJob(context.Background(), events.JobEvent{
		JobID:       "job_oci",
		Kind:        events.KindDeleteOCIObject,
		ObjectKey:   "obj-1",
		RequestedAt: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("handle object delete: %v", err)
	}
	if len(api.calls) != 0 {
		t.Fatalf("api calls = %d, want 0 (object delete bypasses the internal API)", len(api.calls))
	}
	if len(obj.deleted) != 1 || obj.deleted[0] != "obj-1" {
		t.Fatalf("deleted = %v, want [obj-1]", obj.deleted)
	}
}

func TestCleanupHandlerRejectsUnsupportedReason(t *testing.T) {
	api := &fakeCleanupAPI{}
	handler, err := NewCleanupHandler(api, nil)
	if err != nil {
		t.Fatalf("new cleanup handler: %v", err)
	}

	err = handler.HandleJob(context.Background(), cleanupEvent(events.KindExpireSecret, "passphrase"))
	if !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("handle unsupported reason error = %v, want ErrInvalidJob", err)
	}
	if len(api.calls) != 0 {
		t.Fatalf("cleanup calls = %d, want 0", len(api.calls))
	}
}

func TestCleanupHandlerPropagatesClientError(t *testing.T) {
	apiErr := errors.New("api unavailable")
	api := &fakeCleanupAPI{err: apiErr}
	handler, err := NewCleanupHandler(api, nil)
	if err != nil {
		t.Fatalf("new cleanup handler: %v", err)
	}

	err = handler.HandleJob(context.Background(), cleanupEvent(events.KindExpireSecret, ""))
	if !errors.Is(err, apiErr) {
		t.Fatalf("handle cleanup error = %v, want api error", err)
	}
}

func TestProcessorRunsCleanupHandler(t *testing.T) {
	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	api := &fakeCleanupAPI{}
	handler, err := NewCleanupHandler(api, nil)
	if err != nil {
		t.Fatalf("new cleanup handler: %v", err)
	}
	processor := newTestProcessor(t, store, handler, 3)

	result, err := processor.Process(ctx, testJobPayload(t, "job_cleanup"))
	if err != nil {
		t.Fatalf("process cleanup job: %v", err)
	}
	if !result.Started || !result.Succeeded {
		t.Fatalf("result = %+v, want started and succeeded", result)
	}
	if len(api.calls) != 1 {
		t.Fatalf("cleanup calls = %d, want 1", len(api.calls))
	}
	if api.calls[0].JobID != "job_cleanup" || api.calls[0].SecretID != "sec_job_cleanup" {
		t.Fatalf("cleanup request = %+v, want processor job metadata", api.calls[0])
	}

	receipt, err := store.Receipt(ctx, "job_cleanup")
	if err != nil {
		t.Fatalf("load receipt: %v", err)
	}
	if receipt.State != StateSucceeded {
		t.Fatalf("receipt state = %q, want succeeded", receipt.State)
	}
}

func cleanupEvent(kind, reason string) events.JobEvent {
	return events.JobEvent{
		JobID:       "job_1",
		Kind:        kind,
		SecretID:    "sec_1",
		Reason:      reason,
		RequestedAt: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
	}
}

func mapValues(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

type fakeCleanupAPI struct {
	calls []CleanupRequest
	err   error
}

func (a *fakeCleanupAPI) CleanupSecret(_ context.Context, req CleanupRequest) (CleanupResponse, error) {
	a.calls = append(a.calls, req)
	if a.err != nil {
		return CleanupResponse{}, a.err
	}
	return CleanupResponse{Cleaned: true}, nil
}
