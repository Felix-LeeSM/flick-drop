package events

import (
	"context"
	"fmt"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
)

type OutboxPublisher struct {
	store      *OutboxStore
	publisher  MessagePublisher
	retryDelay time.Duration
	now        func() time.Time
}

type PublishResult struct {
	Published int
	Failed    int
}

type OutboxPublisherOptions struct {
	RetryDelay time.Duration
}

func NewOutboxPublisher(store *OutboxStore, publisher MessagePublisher, opts OutboxPublisherOptions) (*OutboxPublisher, error) {
	if store == nil {
		return nil, fmt.Errorf("outbox store is required")
	}
	if publisher == nil {
		return nil, fmt.Errorf("message publisher is required")
	}
	retryDelay := opts.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 30 * time.Second
	}
	return &OutboxPublisher{
		store:      store,
		publisher:  publisher,
		retryDelay: retryDelay,
		now:        func() time.Time { return time.Now().UTC() },
	}, nil
}

func (p *OutboxPublisher) SetNowForTest(now func() time.Time) {
	p.now = now
}

func (p *OutboxPublisher) PublishDue(ctx context.Context, limit int) (PublishResult, error) {
	if limit <= 0 {
		return PublishResult{}, fmt.Errorf("limit must be positive")
	}

	now := p.now().UTC()
	// One transaction fences the whole batch: ClaimDueTx takes SKIP LOCKED row
	// locks, the publish happens while the locks are held, and the marks
	// commit atomically. A concurrent replica's claim skips these rows, and a
	// crash rolls everything back to the due pool.
	tx, err := p.store.db.BeginTx(ctx, nil)
	if err != nil {
		return PublishResult{}, fmt.Errorf("begin outbox publish batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	records, err := p.store.ClaimDueTx(ctx, tx, now, limit)
	if err != nil {
		return PublishResult{}, err
	}

	var result PublishResult
	for _, record := range records {
		if err := p.publisher.Publish(ctx, record.Subject, []byte(record.PayloadJSON)); err != nil {
			if markErr := markOutboxFailedTx(ctx, tx, record.ID, err, now.Add(p.retryDelay), now); markErr != nil {
				return PublishResult{}, markErr
			}
			result.Failed++
			continue
		}
		if err := markOutboxPublishedTx(ctx, tx, record.ID, now); err != nil {
			return PublishResult{}, err
		}
		result.Published++
	}
	if err := tx.Commit(); err != nil {
		return PublishResult{}, fmt.Errorf("commit outbox publish batch: %w", err)
	}
	// Count the batch's outcomes for /metrics. The backlog gauge
	// (flick_outbox_pending) is read from the database at scrape time
	// (telemetry.RegisterAPIStateCollector).
	if result.Published > 0 {
		telemetry.OutboxPublished.WithLabelValues("published").Add(float64(result.Published))
	}
	if result.Failed > 0 {
		telemetry.OutboxPublished.WithLabelValues("failed").Add(float64(result.Failed))
	}
	return result, nil
}
