package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

type MessagePublisher interface {
	Publish(context.Context, string, []byte) error
}

type NATSJetStreamPublisher struct {
	js nats.JetStreamContext
}

func ConnectNATS(url string) (*nats.Conn, error) {
	if url == "" {
		return nil, fmt.Errorf("nats url is required")
	}
	conn, err := nats.Connect(url, nats.Name("burnlink"), nats.Timeout(5*time.Second))
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}
	return conn, nil
}

func NewNATSJetStreamPublisher(conn *nats.Conn) (*NATSJetStreamPublisher, error) {
	if conn == nil {
		return nil, fmt.Errorf("nats connection is required")
	}
	js, err := conn.JetStream()
	if err != nil {
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}
	return &NATSJetStreamPublisher{js: js}, nil
}

func (p *NATSJetStreamPublisher) EnsureStream(ctx context.Context, stream, subject string) error {
	if stream == "" {
		return fmt.Errorf("nats stream is required")
	}
	if subject == "" {
		return fmt.Errorf("nats subject is required")
	}

	info, err := p.js.StreamInfo(stream, nats.Context(ctx))
	if err == nil {
		if streamHasSubject(info.Config.Subjects, subject) {
			return nil
		}
		config := info.Config
		config.Subjects = append(config.Subjects, subject)
		if _, err := p.js.UpdateStream(&config, nats.Context(ctx)); err != nil {
			return fmt.Errorf("update nats stream: %w", err)
		}
		return nil
	}
	if !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("read nats stream info: %w", err)
	}

	_, err = p.js.AddStream(&nats.StreamConfig{
		Name:      stream,
		Subjects:  []string{subject},
		Storage:   nats.FileStorage,
		Retention: nats.WorkQueuePolicy,
	}, nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("add nats stream: %w", err)
	}
	return nil
}

func (p *NATSJetStreamPublisher) Publish(ctx context.Context, subject string, payload []byte) error {
	if subject == "" {
		return fmt.Errorf("nats subject is required")
	}
	if len(payload) == 0 {
		return fmt.Errorf("nats payload is required")
	}
	if _, err := p.js.Publish(subject, payload, nats.Context(ctx)); err != nil {
		return fmt.Errorf("publish nats message: %w", err)
	}
	return nil
}

func streamHasSubject(subjects []string, subject string) bool {
	for _, existing := range subjects {
		if existing == subject {
			return true
		}
	}
	return false
}
