package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// SetupTracing with no endpoint must stay fully off: a usable no-op shutdown and
// no error, so a deployment without a collector starts and stops cleanly.
func TestSetupTracingDisabledByDefault(t *testing.T) {
	shutdown, err := SetupTracing(context.Background(), TracingOptions{ServiceName: "flick-test"})
	if err != nil {
		t.Fatalf("SetupTracing(empty endpoint): %v", err)
	}
	if shutdown == nil {
		t.Fatal("SetupTracing returned a nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown: %v", err)
	}
}

// SetupTracing with an endpoint must construct the exporter, resource, and
// provider without error — resource.Merge of Default()+Schemaless is the easy
// thing to get wrong. The global provider is saved/restored so other tests are
// unaffected, and no spans are exported (nothing reaches the dummy endpoint).
func TestSetupTracingEnabledConstructs(t *testing.T) {
	saved := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(saved) })

	shutdown, err := SetupTracing(context.Background(), TracingOptions{
		ServiceName: "flick-test",
		Endpoint:    "http://127.0.0.1:4318",
	})
	if err != nil {
		t.Fatalf("SetupTracing(endpoint): %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// EndSpan must mark the span Error (and record the exception) only when an error
// is passed, leaving successful spans Unset. Uses a local provider so the global
// tracer state is untouched.
func TestEndSpanRecordsError(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tr := tp.Tracer("test")

	_, okSpan := tr.Start(context.Background(), "ok")
	EndSpan(okSpan, nil)
	_, errSpan := tr.Start(context.Background(), "fail")
	EndSpan(errSpan, errors.New("boom"))

	ended := sr.Ended()
	if len(ended) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(ended))
	}
	if got := ended[0].Status().Code; got != codes.Unset {
		t.Errorf("ok span status = %v, want Unset", got)
	}
	if got := ended[1].Status().Code; got != codes.Error {
		t.Errorf("error span status = %v, want Error", got)
	}
	if len(ended[1].Events()) == 0 {
		t.Error("error span should record an exception event")
	}
}
