package events

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go"
)

func TestConsumeMessagesAcksSuccessfulMessages(t *testing.T) {
	ctx := context.Background()
	message := &fakeMessage{data: []byte(`{"job_id":"job_1"}`)}
	processor := MessageProcessorFunc(func(_ context.Context, payload []byte) (MessageAction, error) {
		if string(payload) != string(message.data) {
			t.Fatalf("payload = %q, want %q", string(payload), string(message.data))
		}
		return MessageAck, nil
	})

	result, err := ConsumeMessages(ctx, []Message{message}, processor)
	if err != nil {
		t.Fatalf("consume messages: %v", err)
	}
	if result.Acked != 1 || message.acked != 1 {
		t.Fatalf("acked = %d message acked = %d, want 1/1", result.Acked, message.acked)
	}
	if message.naked != 0 || message.termed != 0 {
		t.Fatalf("unexpected nak/term = %d/%d", message.naked, message.termed)
	}
}

func TestConsumeMessagesNaksRetryableErrors(t *testing.T) {
	ctx := context.Background()
	message := &fakeMessage{data: []byte(`{"job_id":"job_retry"}`)}
	processor := MessageProcessorFunc(func(context.Context, []byte) (MessageAction, error) {
		return "", errors.New("api unavailable")
	})

	result, err := ConsumeMessages(ctx, []Message{message}, processor)
	if err != nil {
		t.Fatalf("consume messages: %v", err)
	}
	if result.Naked != 1 || message.naked != 1 {
		t.Fatalf("naked = %d message naked = %d, want 1/1", result.Naked, message.naked)
	}
}

func TestConsumeMessagesTermsInvalidPayloads(t *testing.T) {
	ctx := context.Background()
	message := &fakeMessage{data: []byte(`{"payload":{"passphrase":"nope"}}`)}
	processor := MessageProcessorFunc(func(context.Context, []byte) (MessageAction, error) {
		return "", ErrInvalidEvent
	})

	result, err := ConsumeMessages(ctx, []Message{message}, processor)
	if err != nil {
		t.Fatalf("consume messages: %v", err)
	}
	if result.Terminated != 1 || message.termed != 1 {
		t.Fatalf("terminated = %d message termed = %d, want 1/1", result.Terminated, message.termed)
	}
}

func TestConsumeMessagesUsesExplicitTerminalAction(t *testing.T) {
	ctx := context.Background()
	message := &fakeMessage{data: []byte(`{"job_id":"job_dead"}`)}
	processor := MessageProcessorFunc(func(context.Context, []byte) (MessageAction, error) {
		return MessageTerminate, nil
	})

	result, err := ConsumeMessages(ctx, []Message{message}, processor)
	if err != nil {
		t.Fatalf("consume messages: %v", err)
	}
	if result.Terminated != 1 || message.termed != 1 {
		t.Fatalf("terminated = %d message termed = %d, want 1/1", result.Terminated, message.termed)
	}
}

func TestConsumeMessagesReturnsAckErrors(t *testing.T) {
	ctx := context.Background()
	message := &fakeMessage{err: errors.New("ack failed")}
	processor := MessageProcessorFunc(func(context.Context, []byte) (MessageAction, error) {
		return MessageAck, nil
	})

	if _, err := ConsumeMessages(ctx, []Message{message}, processor); err == nil {
		t.Fatal("expected consume error")
	}
}

func TestConsumeMessagesRejectsUnknownAction(t *testing.T) {
	ctx := context.Background()
	message := &fakeMessage{}
	processor := MessageProcessorFunc(func(context.Context, []byte) (MessageAction, error) {
		return MessageAction("bogus"), nil
	})

	if _, err := ConsumeMessages(ctx, []Message{message}, processor); err == nil {
		t.Fatal("expected consume error")
	}
	if message.acked != 0 || message.naked != 0 || message.termed != 0 {
		t.Fatalf("unexpected ack/nak/term = %d/%d/%d", message.acked, message.naked, message.termed)
	}
}

func TestConsumerConfigMatches(t *testing.T) {
	wanted := nats.ConsumerConfig{
		Durable:       "burnlink-worker",
		AckPolicy:     nats.AckExplicitPolicy,
		DeliverPolicy: nats.DeliverAllPolicy,
		FilterSubject: "burnlink.jobs",
		MaxDeliver:    3,
		ReplayPolicy:  nats.ReplayInstantPolicy,
	}
	if !consumerConfigMatches(wanted, wanted) {
		t.Fatal("expected identical config to match")
	}
	existing := wanted
	existing.FilterSubject = "other.jobs"
	if consumerConfigMatches(existing, wanted) {
		t.Fatal("expected mismatched filter subject to differ")
	}
}

type fakeMessage struct {
	data   []byte
	err    error
	acked  int
	naked  int
	termed int
}

func (m *fakeMessage) Data() []byte {
	return m.data
}

func (m *fakeMessage) Ack() error {
	m.acked++
	return m.err
}

func (m *fakeMessage) Nak() error {
	m.naked++
	return m.err
}

func (m *fakeMessage) Term() error {
	m.termed++
	return m.err
}
