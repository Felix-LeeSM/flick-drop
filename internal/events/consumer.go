package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	DefaultConsumerDurable = "burnlink-worker"
	DefaultConsumerBatch   = 8
	DefaultMaxDeliver      = 3
	DefaultFetchWait       = 2 * time.Second
)

type MessageAction string

const (
	MessageAck       MessageAction = "ack"
	MessageRetry     MessageAction = "retry"
	MessageTerminate MessageAction = "terminate"
)

type MessageProcessor interface {
	ProcessMessage(context.Context, []byte) (MessageAction, error)
}

type MessageProcessorFunc func(context.Context, []byte) (MessageAction, error)

func (f MessageProcessorFunc) ProcessMessage(ctx context.Context, payload []byte) (MessageAction, error) {
	return f(ctx, payload)
}

type Message interface {
	Data() []byte
	Ack() error
	Nak() error
	Term() error
}

type ConsumerResult struct {
	Acked      int
	Naked      int
	Terminated int
}

type NATSJetStreamConsumer struct {
	js nats.JetStreamContext
}

func NewNATSJetStreamConsumer(conn *nats.Conn) (*NATSJetStreamConsumer, error) {
	if conn == nil {
		return nil, fmt.Errorf("nats connection is required")
	}
	js, err := conn.JetStream()
	if err != nil {
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}
	return &NATSJetStreamConsumer{js: js}, nil
}

func (c *NATSJetStreamConsumer) EnsureConsumer(ctx context.Context, stream, subject, durable string, maxDeliver int) error {
	if stream == "" {
		return fmt.Errorf("nats stream is required")
	}
	if subject == "" {
		return fmt.Errorf("nats subject is required")
	}
	if durable == "" {
		return fmt.Errorf("nats durable consumer is required")
	}
	if maxDeliver <= 0 {
		maxDeliver = DefaultMaxDeliver
	}

	config := nats.ConsumerConfig{
		Durable:       durable,
		AckPolicy:     nats.AckExplicitPolicy,
		DeliverPolicy: nats.DeliverAllPolicy,
		FilterSubject: subject,
		MaxDeliver:    maxDeliver,
		ReplayPolicy:  nats.ReplayInstantPolicy,
	}

	info, err := c.js.ConsumerInfo(stream, durable, nats.Context(ctx))
	if errors.Is(err, nats.ErrConsumerNotFound) {
		if _, err := c.js.AddConsumer(stream, &config, nats.Context(ctx)); err != nil {
			return fmt.Errorf("add nats consumer: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read nats consumer info: %w", err)
	}
	if consumerConfigMatches(info.Config, config) {
		return nil
	}
	if _, err := c.js.UpdateConsumer(stream, &config, nats.Context(ctx)); err != nil {
		return fmt.Errorf("update nats consumer: %w", err)
	}
	return nil
}

func (c *NATSJetStreamConsumer) PullSubscribe(ctx context.Context, stream, subject, durable string) (*nats.Subscription, error) {
	if stream == "" {
		return nil, fmt.Errorf("nats stream is required")
	}
	if subject == "" {
		return nil, fmt.Errorf("nats subject is required")
	}
	if durable == "" {
		return nil, fmt.Errorf("nats durable consumer is required")
	}
	sub, err := c.js.PullSubscribe(subject, durable, nats.Bind(stream, durable), nats.Context(ctx))
	if err != nil {
		return nil, fmt.Errorf("pull subscribe nats consumer: %w", err)
	}
	return sub, nil
}

func (c *NATSJetStreamConsumer) Fetch(ctx context.Context, sub *nats.Subscription, batch int, maxWait time.Duration) ([]Message, error) {
	if sub == nil {
		return nil, fmt.Errorf("nats subscription is required")
	}
	if batch <= 0 {
		batch = DefaultConsumerBatch
	}
	if maxWait <= 0 {
		maxWait = DefaultFetchWait
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := sub.Fetch(batch, nats.MaxWait(maxWait))
	if err != nil {
		if errors.Is(err, nats.ErrTimeout) {
			return nil, nil
		}
		return nil, fmt.Errorf("fetch nats messages: %w", err)
	}
	messages := make([]Message, 0, len(raw))
	for _, msg := range raw {
		messages = append(messages, natsMessage{msg: msg})
	}
	return messages, nil
}

func ConsumeMessages(ctx context.Context, messages []Message, processor MessageProcessor) (ConsumerResult, error) {
	if processor == nil {
		return ConsumerResult{}, fmt.Errorf("message processor is required")
	}

	var result ConsumerResult
	for _, message := range messages {
		action, processErr := processor.ProcessMessage(ctx, message.Data())
		action, err := resolveMessageAction(action, processErr)
		if err != nil {
			return result, err
		}

		if err := applyMessageAction(message, action); err != nil {
			return result, err
		}
		switch action {
		case MessageAck:
			result.Acked++
		case MessageRetry:
			result.Naked++
		case MessageTerminate:
			result.Terminated++
		}
	}
	return result, nil
}

func resolveMessageAction(action MessageAction, processErr error) (MessageAction, error) {
	switch action {
	case MessageAck, MessageRetry, MessageTerminate:
		return action, nil
	case "":
	default:
		return "", fmt.Errorf("unsupported message action %q", action)
	}
	if errors.Is(processErr, ErrInvalidEvent) {
		return MessageTerminate, nil
	}
	if processErr != nil {
		return MessageRetry, nil
	}
	return MessageAck, nil
}

func applyMessageAction(message Message, action MessageAction) error {
	switch action {
	case MessageAck:
		if err := message.Ack(); err != nil {
			return fmt.Errorf("ack nats message: %w", err)
		}
	case MessageRetry:
		if err := message.Nak(); err != nil {
			return fmt.Errorf("nak nats message: %w", err)
		}
	case MessageTerminate:
		if err := message.Term(); err != nil {
			return fmt.Errorf("term nats message: %w", err)
		}
	default:
		return fmt.Errorf("unsupported message action %q", action)
	}
	return nil
}

func consumerConfigMatches(existing, wanted nats.ConsumerConfig) bool {
	return existing.Durable == wanted.Durable &&
		existing.AckPolicy == wanted.AckPolicy &&
		existing.DeliverPolicy == wanted.DeliverPolicy &&
		existing.FilterSubject == wanted.FilterSubject &&
		existing.MaxDeliver == wanted.MaxDeliver &&
		existing.ReplayPolicy == wanted.ReplayPolicy
}

type natsMessage struct {
	msg *nats.Msg
}

func (m natsMessage) Data() []byte {
	return m.msg.Data
}

func (m natsMessage) Ack() error {
	return m.msg.Ack()
}

func (m natsMessage) Nak() error {
	return m.msg.Nak()
}

func (m natsMessage) Term() error {
	return m.msg.Term()
}
