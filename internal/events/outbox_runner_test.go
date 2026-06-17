package events

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunOutboxPublisherPublishesImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	publisher := &fakeDuePublisher{
		cancel: cancel,
		result: PublishResult{Published: 1},
	}
	var logs []string

	err := RunOutboxPublisher(ctx, publisher, OutboxPublisherLoopOptions{
		Limit:    7,
		Interval: time.Hour,
		Logf: func(format string, args ...any) {
			logs = append(logs, format)
		},
	})
	if err != nil {
		t.Fatalf("run outbox publisher: %v", err)
	}
	if publisher.calls != 1 {
		t.Fatalf("publish calls = %d, want 1", publisher.calls)
	}
	if publisher.limit != 7 {
		t.Fatalf("publish limit = %d, want 7", publisher.limit)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "published=%d failed=%d") {
		t.Fatalf("logs = %#v, want published batch log format", logs)
	}
}

func TestRunOutboxPublisherLogsAndContinuesAfterPublishError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	publisher := &fakeDuePublisher{
		cancel:      cancel,
		cancelAfter: 2,
		errs:        []error{errors.New("nats unavailable")},
	}
	var logs []string

	err := RunOutboxPublisher(ctx, publisher, OutboxPublisherLoopOptions{
		Limit:    3,
		Interval: time.Millisecond,
		Logf: func(format string, args ...any) {
			logs = append(logs, format)
		},
	})
	if err != nil {
		t.Fatalf("run outbox publisher: %v", err)
	}
	if publisher.calls != 2 {
		t.Fatalf("publish calls = %d, want 2", publisher.calls)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "outbox publisher error") {
		t.Fatalf("logs = %#v, want one publish error log", logs)
	}
}

func TestRunOutboxPublisherRequiresPublisher(t *testing.T) {
	err := RunOutboxPublisher(context.Background(), nil, OutboxPublisherLoopOptions{})
	if err == nil {
		t.Fatal("run outbox publisher error = nil, want error")
	}
}

type fakeDuePublisher struct {
	cancel      context.CancelFunc
	cancelAfter int
	result      PublishResult
	errs        []error
	calls       int
	limit       int
}

func (p *fakeDuePublisher) PublishDue(_ context.Context, limit int) (PublishResult, error) {
	p.calls++
	p.limit = limit
	if p.cancelAfter == 0 || p.calls >= p.cancelAfter {
		p.cancel()
	}
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		return PublishResult{}, err
	}
	return p.result, nil
}
