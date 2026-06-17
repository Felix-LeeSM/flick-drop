package events

import (
	"context"
	"fmt"
	"time"
)

const (
	DefaultOutboxPublisherLimit    = 50
	DefaultOutboxPublisherInterval = 2 * time.Second
)

type OutboxPublisherLoopOptions struct {
	Limit    int
	Interval time.Duration
	Logf     func(string, ...any)
}

type duePublisher interface {
	PublishDue(context.Context, int) (PublishResult, error)
}

func RunOutboxPublisher(ctx context.Context, publisher duePublisher, opts OutboxPublisherLoopOptions) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if publisher == nil {
		return fmt.Errorf("outbox publisher is required")
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultOutboxPublisherLimit
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultOutboxPublisherInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		publishOutboxBatch(ctx, publisher, limit, opts.Logf)

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func publishOutboxBatch(ctx context.Context, publisher duePublisher, limit int, logf func(string, ...any)) {
	result, err := publisher.PublishDue(ctx, limit)
	if err != nil {
		if logf != nil {
			logf("outbox publisher error: %v", err)
		}
		return
	}
	if logf != nil && (result.Published > 0 || result.Failed > 0) {
		logf("outbox publisher batch: published=%d failed=%d", result.Published, result.Failed)
	}
}
