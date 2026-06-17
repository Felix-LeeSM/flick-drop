package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Felix-LeeSM/burn-links/internal/events"
)

func TestConsumerRunnerProcessesFetchedMessagesAndExitsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	message := &runnerMessage{data: []byte(`{"job_id":"job_1"}`)}
	consumer := &fakeMessageConsumer{
		batches: [][]events.Message{{message}},
		cancel:  cancel,
	}
	var processed [][]byte
	processor := events.MessageProcessorFunc(func(_ context.Context, payload []byte) (events.MessageAction, error) {
		processed = append(processed, payload)
		return events.MessageAck, nil
	})

	runner, err := NewConsumerRunner(consumer, processor, RunnerOptions{
		Stream:    "BURNLINK_JOBS",
		Subject:   "burnlink.jobs",
		BatchSize: 2,
		FetchWait: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new consumer runner: %v", err)
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("run consumer: %v", err)
	}
	if !consumer.ensureCalled || !consumer.pullCalled {
		t.Fatalf("ensure/pull called = %v/%v, want true/true", consumer.ensureCalled, consumer.pullCalled)
	}
	if consumer.stream != "BURNLINK_JOBS" || consumer.subject != "burnlink.jobs" {
		t.Fatalf("consumer route = %q/%q, want BURNLINK_JOBS/burnlink.jobs", consumer.stream, consumer.subject)
	}
	if consumer.durable != events.DefaultConsumerDurable {
		t.Fatalf("durable = %q, want %q", consumer.durable, events.DefaultConsumerDurable)
	}
	if consumer.maxDeliver != events.DefaultMaxDeliver {
		t.Fatalf("max deliver = %d, want %d", consumer.maxDeliver, events.DefaultMaxDeliver)
	}
	if consumer.batch != 2 || consumer.maxWait != time.Millisecond {
		t.Fatalf("fetch options = %d/%s, want 2/1ms", consumer.batch, consumer.maxWait)
	}
	if len(processed) != 1 || string(processed[0]) != string(message.data) {
		t.Fatalf("processed payloads = %q, want one message", processed)
	}
	if !message.acked || message.naked || message.terminated {
		t.Fatalf("message disposition ack/nak/term = %v/%v/%v, want true/false/false", message.acked, message.naked, message.terminated)
	}
}

func TestConsumerRunnerPropagatesFetchError(t *testing.T) {
	fetchErr := errors.New("nats unavailable")
	consumer := &fakeMessageConsumer{fetchErr: fetchErr}
	processor := events.MessageProcessorFunc(func(context.Context, []byte) (events.MessageAction, error) {
		t.Fatal("processor should not run")
		return "", nil
	})

	runner, err := NewConsumerRunner(consumer, processor, RunnerOptions{
		Stream:  "BURNLINK_JOBS",
		Subject: "burnlink.jobs",
	})
	if err != nil {
		t.Fatalf("new consumer runner: %v", err)
	}

	if err := runner.Run(context.Background()); !errors.Is(err, fetchErr) {
		t.Fatalf("run error = %v, want fetch error", err)
	}
}

func TestConsumerRunnerRejectsMissingInputs(t *testing.T) {
	consumer := &fakeMessageConsumer{}
	processor := events.MessageProcessorFunc(func(context.Context, []byte) (events.MessageAction, error) {
		return events.MessageAck, nil
	})

	if _, err := NewConsumerRunner(nil, processor, RunnerOptions{Stream: "s", Subject: "j"}); err == nil {
		t.Fatal("missing consumer error = nil, want error")
	}
	if _, err := NewConsumerRunner(consumer, nil, RunnerOptions{Stream: "s", Subject: "j"}); err == nil {
		t.Fatal("missing processor error = nil, want error")
	}
	if _, err := NewConsumerRunner(consumer, processor, RunnerOptions{Subject: "j"}); err == nil {
		t.Fatal("missing stream error = nil, want error")
	}
	if _, err := NewConsumerRunner(consumer, processor, RunnerOptions{Stream: "s"}); err == nil {
		t.Fatal("missing subject error = nil, want error")
	}
}

type fakeMessageConsumer struct {
	ensureCalled bool
	pullCalled   bool
	stream       string
	subject      string
	durable      string
	maxDeliver   int
	batch        int
	maxWait      time.Duration
	batches      [][]events.Message
	fetchErr     error
	fetches      int
	cancel       context.CancelFunc
}

func (c *fakeMessageConsumer) EnsureConsumer(_ context.Context, stream, subject, durable string, maxDeliver int) error {
	c.ensureCalled = true
	c.stream = stream
	c.subject = subject
	c.durable = durable
	c.maxDeliver = maxDeliver
	return nil
}

func (c *fakeMessageConsumer) PullSubscribe(_ context.Context, stream, subject, durable string) (MessageSubscription, error) {
	c.pullCalled = true
	c.stream = stream
	c.subject = subject
	c.durable = durable
	return "subscription", nil
}

func (c *fakeMessageConsumer) Fetch(_ context.Context, _ MessageSubscription, batch int, maxWait time.Duration) ([]events.Message, error) {
	c.fetches++
	c.batch = batch
	c.maxWait = maxWait
	if c.fetchErr != nil {
		return nil, c.fetchErr
	}
	if len(c.batches) == 0 {
		if c.cancel != nil {
			c.cancel()
		}
		return nil, nil
	}
	messages := c.batches[0]
	c.batches = c.batches[1:]
	if len(c.batches) == 0 && c.cancel != nil {
		defer c.cancel()
	}
	return messages, nil
}

type runnerMessage struct {
	data       []byte
	acked      bool
	naked      bool
	terminated bool
}

func (m *runnerMessage) Data() []byte {
	return m.data
}

func (m *runnerMessage) Ack() error {
	m.acked = true
	return nil
}

func (m *runnerMessage) Nak() error {
	m.naked = true
	return nil
}

func (m *runnerMessage) Term() error {
	m.terminated = true
	return nil
}
