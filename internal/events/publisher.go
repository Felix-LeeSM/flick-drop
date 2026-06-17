package events

import (
	"context"
	"fmt"
	"time"
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
	records, err := p.store.ListDue(ctx, now, limit)
	if err != nil {
		return PublishResult{}, err
	}

	var result PublishResult
	for _, record := range records {
		if err := p.publisher.Publish(ctx, record.Subject, []byte(record.PayloadJSON)); err != nil {
			if markErr := p.store.MarkFailed(ctx, record.ID, err, now.Add(p.retryDelay)); markErr != nil {
				return result, markErr
			}
			result.Failed++
			continue
		}
		if err := p.store.MarkPublished(ctx, record.ID); err != nil {
			return result, err
		}
		result.Published++
	}
	return result, nil
}
