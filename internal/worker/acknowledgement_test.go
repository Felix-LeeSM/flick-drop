package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
)

type acknowledgeFunc func(context.Context, events.JobEvent) error

func (f acknowledgeFunc) AcknowledgeObjectCleanup(ctx context.Context, event events.JobEvent) error {
	return f(ctx, event)
}

func TestTerminalObjectAcknowledgementRecoversAfterRestart(t *testing.T) {
	for _, dead := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "dead"}[dead], func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "worker.db")
			store := openRecoveryStore(t, path)
			handler := &fakeJobHandler{}
			if dead {
				handler.err = errors.New("delete failed")
			}
			ackCalls := 0
			ack := acknowledgeFunc(func(ctx context.Context, event events.JobEvent) error {
				ackCalls++
				if _, ok := ctx.Deadline(); !ok {
					t.Error("acknowledgement has no deadline")
				}
				receipt, err := store.Receipt(ctx, event.JobID)
				if err != nil {
					t.Fatal(err)
				}
				want := StateSucceeded
				if dead {
					want = StateDead
				}
				if receipt.State != want {
					t.Fatalf("ack before terminal receipt: %s", receipt.State)
				}
				if ackCalls == 1 {
					return errors.New("api unavailable")
				}
				return nil
			})
			proc, err := NewProcessor(store, handler, ProcessorOptions{MaxAttempts: 1, Acknowledger: ack})
			if err != nil {
				t.Fatal(err)
			}
			payload, err := (events.JobEvent{JobID: "cleanup", Kind: events.KindDeleteOCIObject, ObjectKey: "managed/secrets/late", RequestedAt: time.Now().UTC()}).JSON()
			if err != nil {
				t.Fatal(err)
			}
			message := &runnerMessage{data: payload}
			if _, err := events.ConsumeMessages(ctx, []events.Message{message}, proc); err != nil {
				t.Fatal(err)
			}
			if message.acked || message.terminated || !message.naked {
				t.Fatal("API failure acknowledged or terminated NATS delivery")
			}
			if err := store.db.Close(); err != nil {
				t.Fatal(err)
			}
			store = openRecoveryStore(t, path)
			proc, err = NewProcessor(store, handler, ProcessorOptions{MaxAttempts: 1, Acknowledger: ack})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				action, err := proc.ProcessMessage(ctx, payload)
				want := events.MessageAck
				if dead {
					want = events.MessageTerminate
				}
				if err != nil || action != want {
					t.Fatalf("terminal redelivery: %q %v", action, err)
				}
			}
			if len(handler.calls) != 1 || ackCalls != 3 {
				t.Fatalf("delete calls=%d ack calls=%d", len(handler.calls), ackCalls)
			}
			receipt, err := store.Receipt(ctx, "cleanup")
			if err != nil || receipt.Attempts != 1 {
				t.Fatalf("ack failures spent handler attempts: %v %v", receipt, err)
			}
		})
	}
}

type acknowledgementTransport func(*http.Request) (*http.Response, error)

func (f acknowledgementTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCleanupClientAcknowledgementContract(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound, http.StatusServiceUnavailable} {
		client, err := NewCleanupClient(CleanupClientOptions{BaseURL: "http://api.example", InternalToken: "test-token", HTTPClient: &http.Client{Transport: acknowledgementTransport(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodPost || r.URL.Path != "/internal/object-reconciliation/ack" || r.Header.Get("X-Flick-Internal-Token") != "test-token" {
				t.Fatalf("unexpected acknowledgement request")
			}
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if len(payload) != 2 || payload["job_id"] != "job" || payload["object_key"] != "managed/secrets/key" {
				t.Fatalf("unexpected acknowledgement payload: %v", payload)
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		err = client.AcknowledgeObjectCleanup(context.Background(), events.JobEvent{JobID: "job", Kind: events.KindDeleteOCIObject, ObjectKey: "managed/secrets/key"})
		if (err == nil) != (status == http.StatusNoContent) {
			t.Fatalf("status %d error %v", status, err)
		}
	}
	handler, err := NewCleanupHandler(&fakeCleanupAPI{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleJob(context.Background(), events.JobEvent{Kind: events.KindDeleteOCIObject, ObjectKey: "managed/secrets/key"}); err == nil {
		t.Fatal("disabled storage reported successful managed deletion")
	}
}
