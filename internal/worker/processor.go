package worker

import (
	"context"
	"fmt"

	"github.com/Felix-LeeSM/burn-links/internal/events"
)

const DefaultMaxAttempts = 3

type JobHandler interface {
	HandleJob(context.Context, events.JobEvent) error
}

type JobHandlerFunc func(context.Context, events.JobEvent) error

func (f JobHandlerFunc) HandleJob(ctx context.Context, event events.JobEvent) error {
	return f(ctx, event)
}

type Processor struct {
	store       *ReceiptStore
	handler     JobHandler
	maxAttempts int
}

type ProcessorOptions struct {
	MaxAttempts int
}

type ProcessResult struct {
	Attempt           int
	Started           bool
	Succeeded         bool
	Failed            bool
	DeadLettered      bool
	AlreadySucceeded  bool
	AlreadyProcessing bool
	AlreadyDead       bool
}

func NewProcessor(store *ReceiptStore, handler JobHandler, opts ProcessorOptions) (*Processor, error) {
	if store == nil {
		return nil, fmt.Errorf("receipt store is required")
	}
	if handler == nil {
		return nil, fmt.Errorf("job handler is required")
	}
	maxAttempts := opts.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = DefaultMaxAttempts
	}
	if maxAttempts < 0 {
		return nil, fmt.Errorf("max attempts must be positive")
	}
	return &Processor{
		store:       store,
		handler:     handler,
		maxAttempts: maxAttempts,
	}, nil
}

func (p *Processor) Process(ctx context.Context, payloadJSON []byte) (ProcessResult, error) {
	event, err := events.DecodeJobEvent(payloadJSON)
	if err != nil {
		return ProcessResult{}, err
	}
	canonicalPayload, err := event.JSON()
	if err != nil {
		return ProcessResult{}, err
	}

	started, err := p.store.Start(ctx, event.JobID, event.Kind)
	if err != nil {
		switch err {
		case ErrJobProcessing:
			return ProcessResult{AlreadyProcessing: true}, nil
		case ErrJobDead:
			return ProcessResult{AlreadyDead: true, DeadLettered: true}, nil
		default:
			return ProcessResult{}, err
		}
	}
	if started.AlreadySucceeded {
		return ProcessResult{AlreadySucceeded: true, Succeeded: true}, nil
	}

	result := ProcessResult{
		Attempt: started.Attempt.Attempt,
		Started: true,
	}
	if err := p.handler.HandleJob(ctx, event); err != nil {
		return p.finishFailed(ctx, event, string(canonicalPayload), started.Attempt, result, err)
	}
	if err := p.store.MarkSucceeded(ctx, started.Attempt.ID); err != nil {
		return result, err
	}
	result.Succeeded = true
	return result, nil
}

func (p *Processor) ProcessMessage(ctx context.Context, payloadJSON []byte) (events.MessageAction, error) {
	result, err := p.Process(ctx, payloadJSON)
	if err != nil {
		return "", err
	}
	if result.DeadLettered {
		return events.MessageTerminate, nil
	}
	return events.MessageAck, nil
}

func (p *Processor) finishFailed(
	ctx context.Context,
	event events.JobEvent,
	payloadJSON string,
	attempt Attempt,
	result ProcessResult,
	jobErr error,
) (ProcessResult, error) {
	if err := p.store.MarkFailed(ctx, attempt.ID, jobErr); err != nil {
		return result, err
	}
	result.Failed = true

	if attempt.Attempt < p.maxAttempts {
		return result, jobErr
	}
	if err := p.store.DeadLetter(ctx, event.JobID, event.Kind, payloadJSON, jobErr); err != nil {
		return result, err
	}
	result.DeadLettered = true
	return result, nil
}
