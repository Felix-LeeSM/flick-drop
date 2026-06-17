package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/Felix-LeeSM/burn-links/internal/events"
	"github.com/nats-io/nats.go"
)

type MessageSubscription any

type MessageConsumer interface {
	EnsureConsumer(ctx context.Context, stream, subject, durable string, maxDeliver int) error
	PullSubscribe(ctx context.Context, stream, subject, durable string) (MessageSubscription, error)
	Fetch(ctx context.Context, sub MessageSubscription, batch int, maxWait time.Duration) ([]events.Message, error)
}

type NATSConsumerAdapter struct {
	consumer *events.NATSJetStreamConsumer
}

func NewNATSConsumerAdapter(consumer *events.NATSJetStreamConsumer) (*NATSConsumerAdapter, error) {
	if consumer == nil {
		return nil, fmt.Errorf("nats consumer is required")
	}
	return &NATSConsumerAdapter{consumer: consumer}, nil
}

func (a *NATSConsumerAdapter) EnsureConsumer(ctx context.Context, stream, subject, durable string, maxDeliver int) error {
	return a.consumer.EnsureConsumer(ctx, stream, subject, durable, maxDeliver)
}

func (a *NATSConsumerAdapter) PullSubscribe(ctx context.Context, stream, subject, durable string) (MessageSubscription, error) {
	return a.consumer.PullSubscribe(ctx, stream, subject, durable)
}

func (a *NATSConsumerAdapter) Fetch(ctx context.Context, sub MessageSubscription, batch int, maxWait time.Duration) ([]events.Message, error) {
	natsSub, ok := sub.(*nats.Subscription)
	if !ok {
		return nil, fmt.Errorf("nats subscription has unexpected type %T", sub)
	}
	return a.consumer.Fetch(ctx, natsSub, batch, maxWait)
}

type ConsumerRunner struct {
	consumer  MessageConsumer
	processor events.MessageProcessor
	opts      RunnerOptions
}

type RunnerOptions struct {
	Stream     string
	Subject    string
	Durable    string
	MaxDeliver int
	BatchSize  int
	FetchWait  time.Duration
}

func NewConsumerRunner(consumer MessageConsumer, processor events.MessageProcessor, opts RunnerOptions) (*ConsumerRunner, error) {
	if consumer == nil {
		return nil, fmt.Errorf("message consumer is required")
	}
	if processor == nil {
		return nil, fmt.Errorf("message processor is required")
	}
	if opts.Stream == "" {
		return nil, fmt.Errorf("nats stream is required")
	}
	if opts.Subject == "" {
		return nil, fmt.Errorf("nats subject is required")
	}
	if opts.Durable == "" {
		opts.Durable = events.DefaultConsumerDurable
	}
	if opts.MaxDeliver <= 0 {
		opts.MaxDeliver = events.DefaultMaxDeliver
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = events.DefaultConsumerBatch
	}
	if opts.FetchWait <= 0 {
		opts.FetchWait = events.DefaultFetchWait
	}
	return &ConsumerRunner{
		consumer:  consumer,
		processor: processor,
		opts:      opts,
	}, nil
}

func (r *ConsumerRunner) Run(ctx context.Context) error {
	if err := r.consumer.EnsureConsumer(ctx, r.opts.Stream, r.opts.Subject, r.opts.Durable, r.opts.MaxDeliver); err != nil {
		return err
	}
	sub, err := r.consumer.PullSubscribe(ctx, r.opts.Stream, r.opts.Subject, r.opts.Durable)
	if err != nil {
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		messages, err := r.consumer.Fetch(ctx, sub, r.opts.BatchSize, r.opts.FetchWait)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if len(messages) == 0 {
			continue
		}
		if _, err := events.ConsumeMessages(ctx, messages, r.processor); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

var _ MessageConsumer = (*NATSConsumerAdapter)(nil)
