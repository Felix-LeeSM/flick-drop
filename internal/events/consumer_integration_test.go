package events

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestNATSJetStreamConsumerIntegration(t *testing.T) {
	if os.Getenv("FLICK_NATS_INTEGRATION") != "1" {
		t.Skip("set FLICK_NATS_INTEGRATION=1 to run against local NATS")
	}
	url := os.Getenv("FLICK_NATS_URL")
	if url == "" {
		url = "nats://127.0.0.1:4222"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := ConnectNATS(url)
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Drain()
	})

	suffix := time.Now().UnixNano()
	stream := fmt.Sprintf("FLICK_JOBS_CONSUMER_%d", suffix)
	subject := fmt.Sprintf("flick.jobs.consumer.%d", suffix)
	durable := "flick-worker-test"

	publisher, err := NewNATSJetStreamPublisher(conn)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	if err := publisher.EnsureStream(ctx, stream, subject); err != nil {
		t.Fatalf("ensure stream: %v", err)
	}

	consumer, err := NewNATSJetStreamConsumer(conn)
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}
	t.Cleanup(func() {
		_ = consumer.js.DeleteStream(stream)
	})
	if err := consumer.EnsureConsumer(ctx, stream, subject, durable, 3); err != nil {
		t.Fatalf("ensure consumer: %v", err)
	}
	sub, err := consumer.PullSubscribe(ctx, stream, subject, durable)
	if err != nil {
		t.Fatalf("pull subscribe: %v", err)
	}
	t.Cleanup(func() {
		_ = sub.Unsubscribe()
	})

	payload := []byte(`{"job_id":"job_consumer","kind":"delete_secret","secret_id":"sec_consumer","requested_at":"2026-06-17T12:00:00Z"}`)
	if err := publisher.Publish(ctx, subject, payload); err != nil {
		t.Fatalf("publish: %v", err)
	}

	messages, err := consumer.Fetch(ctx, sub, 1, 2*time.Second)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}

	result, err := ConsumeMessages(ctx, messages, MessageProcessorFunc(func(_ context.Context, got []byte) (MessageAction, error) {
		if string(got) != string(payload) {
			t.Fatalf("payload = %q, want %q", string(got), string(payload))
		}
		return MessageAck, nil
	}))
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if result.Acked != 1 {
		t.Fatalf("acked = %d, want 1", result.Acked)
	}
}
