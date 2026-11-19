package events

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceContextRoundTrip(t *testing.T) {
	// Mutates global otel state; do not t.Parallel.
	prevProp := otel.GetTextMapPropagator()
	prevTP := otel.GetTracerProvider()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTextMapPropagator(prevProp)
		otel.SetTracerProvider(prevTP)
		_ = tp.Shutdown(context.Background())
	})

	ctx, producer := tp.Tracer("test").Start(context.Background(), "producer")
	defer producer.End()

	event := JobEvent{
		JobID:       "job_1",
		Kind:        KindDeleteSecret,
		SecretID:    "sec_1",
		RequestedAt: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
	}
	injectTraceContext(ctx, &event)
	if len(event.TraceContext) == 0 {
		t.Fatal("injectTraceContext wrote no trace context for an active span")
	}

	raw, err := event.JSON()
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	decoded, err := DecodeJobEvent(raw)
	if err != nil {
		t.Fatalf("decode event: %v", err)
	}

	sc := trace.SpanContextFromContext(ContextWithTrace(context.Background(), decoded))
	if got, want := sc.TraceID(), producer.SpanContext().TraceID(); got != want {
		t.Errorf("trace id = %s, want producer %s", got, want)
	}
	if got, want := sc.SpanID(), producer.SpanContext().SpanID(); got != want {
		t.Errorf("parent span id = %s, want producer %s", got, want)
	}
	if !sc.IsRemote() {
		t.Error("extracted span context should be marked remote")
	}
}

func TestTraceContextOffIsNoop(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	event := JobEvent{
		JobID:       "job_1",
		Kind:        KindDeleteSecret,
		SecretID:    "sec_1",
		RequestedAt: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
	}
	injectTraceContext(context.Background(), &event)
	if event.TraceContext != nil {
		t.Errorf("trace context = %v, want nil when tracing off", event.TraceContext)
	}
	if got := ContextWithTrace(context.Background(), event); got != context.Background() {
		t.Error("ContextWithTrace should return ctx unchanged for an untraced event")
	}
}
