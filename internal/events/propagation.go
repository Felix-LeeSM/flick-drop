package events

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Trace context rides in the job payload because the async publisher runs after the
// enqueuing span ends. No-op until SetupTracing installs the W3C propagator.

func injectTraceContext(ctx context.Context, event *JobEvent) {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) > 0 {
		event.TraceContext = carrier
	}
}

func ContextWithTrace(ctx context.Context, event JobEvent) context.Context {
	if len(event.TraceContext) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(event.TraceContext))
}
