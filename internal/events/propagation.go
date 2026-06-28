package events

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// The events pipeline is async: the API/reaper enqueues a job into the outbox
// inside a request/reaper span, but a separate goroutine publishes it to NATS
// later, by which time that span is gone. So the trace context is captured at
// enqueue time (injectTraceContext) into the job payload and re-established at
// consume time (ContextWithTrace), letting the worker span continue the
// producer's trace across the hop. Both are no-ops when tracing is off (the
// global propagator is a no-op until SetupTracing installs the W3C one), so an
// untraced deployment carries no trace_context field and pays nothing.

// injectTraceContext writes the active span's W3C trace context into event so it
// survives the outbox row and the NATS publish. No span / tracing off → nothing
// written and the omitempty field stays absent.
func injectTraceContext(ctx context.Context, event *JobEvent) {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) > 0 {
		event.TraceContext = carrier
	}
}

// ContextWithTrace returns ctx with the trace context the event was enqueued
// under, so a span started from it continues the producer's trace. Returns ctx
// unchanged when the event carries no trace context (untraced or tracing off).
func ContextWithTrace(ctx context.Context, event JobEvent) context.Context {
	if len(event.TraceContext) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(event.TraceContext))
}
