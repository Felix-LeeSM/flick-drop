package worker

import (
	"context"
	"fmt"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const DefaultMaxAttempts = 3

// tracer instruments job processing. With tracing off (no OTLP endpoint) it is
// OTel's no-op, so tracer.Start costs nothing. The worker.Process span continues
// the producer's trace via the job payload's trace context (see Process and
// events.ContextWithTrace), so a job is followed end to end across the NATS hop.
var tracer = otel.Tracer("github.com/Felix-LeeSM/flick-drop/internal/worker")

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

func (p *Processor) Process(ctx context.Context, payloadJSON []byte) (_ ProcessResult, err error) {
	event, decodeErr := events.DecodeJobEvent(payloadJSON)
	if decodeErr != nil {
		// Malformed message: no kind or trace context to attach. Return pre-span
		// (the consumer terminates it) rather than emit an orphan worker span.
		return ProcessResult{}, decodeErr
	}

	// Continue the producer's trace across the async NATS hop (#133): a child of
	// the enqueuing span when the job carries a trace context, else a root.
	// SpanKindConsumer marks the receive side of the messaging link.
	ctx = events.ContextWithTrace(ctx, event)
	ctx, span := tracer.Start(ctx, "worker.Process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("job.kind", event.Kind)),
	)
	defer func() { telemetry.EndSpan(span, err) }()

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
	telemetry.JobsProcessed.WithLabelValues(event.Kind, "succeeded").Inc()
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
		telemetry.JobsProcessed.WithLabelValues(event.Kind, "failed").Inc()
		return result, jobErr
	}
	if err := p.store.DeadLetter(ctx, event.JobID, event.Kind, payloadJSON, jobErr); err != nil {
		return result, err
	}
	result.DeadLettered = true
	telemetry.JobsProcessed.WithLabelValues(event.Kind, "dead").Inc()
	return result, nil
}
