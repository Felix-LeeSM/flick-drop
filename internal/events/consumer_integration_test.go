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

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
	// Upgrading an existing deployment must remove the broker delivery limit;
	// only the worker knows which deliveries were real handler failures.
	if err := consumer.EnsureConsumer(ctx, stream, subject, durable, 0); err != nil {
		t.Fatalf("upgrade consumer: %v", err)
	}
	info, err := consumer.js.ConsumerInfo(stream, durable)
	if err != nil || info.Config.MaxDeliver != -1 {
		t.Fatalf("consumer = %+v, error = %v, want unlimited delivery", info, err)
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
	// Retry more than the former limit, then complete. The first retry uses
	// the real delayed NAK; the rest use immediate NAK to keep this test short.
	for retry := range 4 {
		if retry == 0 {
			err = messages[0].Nak()
		} else {
			err = messages[0].(natsMessage).msg.Nak()
		}
		if err != nil {
			t.Fatalf("retry %d: %v", retry, err)
		}
		messages, err = consumer.Fetch(ctx, sub, 1, DefaultRetryDelay+time.Second)
		if err != nil || len(messages) != 1 {
			t.Fatalf("retry %d: messages = %d, error = %v", retry, len(messages), err)
		}
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
