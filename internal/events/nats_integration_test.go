package events

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestNATSJetStreamPublisherIntegration(t *testing.T) {
	if os.Getenv("FLICK_NATS_INTEGRATION") != "1" {
		t.Skip("set FLICK_NATS_INTEGRATION=1 to run against local NATS")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := os.Getenv("FLICK_NATS_URL")
	if url == "" {
		url = "nats://127.0.0.1:4222"
	}

	conn, err := ConnectNATS(url)
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	t.Cleanup(conn.Close)

	publisher, err := NewNATSJetStreamPublisher(conn)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	stream := "FLICK_TEST_" + suffix
	subject := "flick.test." + suffix
	if err := publisher.EnsureStream(ctx, stream, subject); err != nil {
		t.Fatalf("ensure stream: %v", err)
	}
	t.Cleanup(func() {
		_ = publisher.js.DeleteStream(stream)
	})

	payload := []byte(`{"job_id":"job_1","kind":"delete_secret","secret_id":"sec_1","requested_at":"2026-06-17T00:00:00Z"}`)
	if err := publisher.Publish(ctx, subject, payload); err != nil {
		t.Fatalf("publish: %v", err)
	}
}
