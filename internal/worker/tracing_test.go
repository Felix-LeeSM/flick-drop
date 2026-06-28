package worker

import (
	"context"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Process must continue the producer's trace: when the job payload carries a
// trace context, the worker.Process span belongs to that trace and is a child of
// the enqueuing span. Installs a recording provider + W3C propagator globally
// (the package tracer delegates to it); restored after.
func TestProcessContinuesProducerTrace(t *testing.T) {
	// Mutates global otel state (provider + propagator); do not t.Parallel.
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		_ = tp.Shutdown(context.Background())
	})

	const traceID = "0123456789abcdef0123456789abcdef"
	const spanID = "0123456789abcdef"
	payload, err := (events.JobEvent{
		JobID:        "job_trace",
		Kind:         events.KindExpireSecret,
		SecretID:     "sec_trace",
		RequestedAt:  time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
		TraceContext: map[string]string{"traceparent": "00-" + traceID + "-" + spanID + "-01"},
	}).JSON()
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	ctx := context.Background()
	store := newTestReceiptStore(t, ctx)
	proc := newTestProcessor(t, store, &fakeJobHandler{}, 3)
	if _, err := proc.Process(ctx, payload); err != nil {
		t.Fatalf("process: %v", err)
	}

	var span sdktrace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.Name() == "worker.Process" {
			span = s
		}
	}
	if span == nil {
		t.Fatal("no worker.Process span recorded")
	}
	if got := span.SpanContext().TraceID().String(); got != traceID {
		t.Errorf("worker span trace id = %s, want producer trace %s", got, traceID)
	}
	if got := span.Parent().SpanID().String(); got != spanID {
		t.Errorf("worker span parent = %s, want producer span %s", got, spanID)
	}
}
