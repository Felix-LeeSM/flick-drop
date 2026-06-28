package httpapi

import (
	"context"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The tracing middleware's fragile, load-bearing bits: the matched chi route
// template is read AFTER next() (so spans carry "/api/secrets/{id}", not the
// high-cardinality concrete path), the status code is captured, and probe
// endpoints get no span at all. A recording provider is installed globally so
// the package's delegating tracer routes to it; restored after the test.
func TestTracingMiddlewareRecordsRouteAndStatus(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})

	router := newTestRouter(t)

	// Unknown id still routes through the "/api/secrets/{id}" template (handler
	// returns 404), so the matched route — not the concrete id — must be recorded.
	if resp := performJSON(t, router, http.MethodGet, "/api/secrets/does-not-exist", nil, nil); resp.Code != http.StatusNotFound {
		t.Fatalf("get unknown status = %d, want 404", resp.Code)
	}
	// Probe endpoints are skipped by the middleware — they must produce no span.
	if resp := performJSON(t, router, http.MethodGet, "/healthz", nil, nil); resp.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.Code)
	}

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("recorded %d spans, want 1 (healthz must be skipped)", len(ended))
	}
	span := ended[0]
	if got, want := span.Name(), "GET /api/secrets/{id}"; got != want {
		t.Errorf("span name = %q, want %q", got, want)
	}

	var route string
	var status int64
	for _, kv := range span.Attributes() {
		switch kv.Key {
		case "http.route":
			route = kv.Value.AsString()
		case "http.response.status_code":
			status = kv.Value.AsInt64()
		}
	}
	if route != "/api/secrets/{id}" {
		t.Errorf("http.route = %q, want /api/secrets/{id}", route)
	}
	if status != http.StatusNotFound {
		t.Errorf("http.response.status_code = %d, want 404", status)
	}
}
