package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestSetupTracingExportsToConfiguredPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "bare origin", want: "/v1/traces"},
		{name: "explicit root", path: "/", want: "/"},
		{name: "explicit signal", path: "/v1/traces", want: "/v1/traces"},
		{name: "custom path", path: "/collector/traces", want: "/collector/traces"},
		{name: "custom trailing slash", path: "/collector/traces/", want: "/collector/traces/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, propagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
			t.Cleanup(func() {
				otel.SetTracerProvider(provider)
				otel.SetTextMapPropagator(propagator)
			})
			paths := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-protobuf" {
					t.Errorf("unexpected export: method=%s content-type=%q", r.Method, r.Header.Get("Content-Type"))
				}
				if n, err := io.Copy(io.Discard, r.Body); err != nil || n == 0 {
					t.Errorf("export body: bytes=%d error=%v", n, err)
				}
				paths <- r.URL.Path
				w.Header().Set("Content-Type", "application/x-protobuf")
			}))
			defer server.Close()
			// FLICK_OTLP_ENDPOINT takes precedence over both standard OTEL endpoints.
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL+"/env")
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", server.URL+"/env/traces")

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			shutdown, err := SetupTracing(ctx, TracingOptions{
				ServiceName: "flick-test",
				Endpoint:    server.URL + tc.path,
			})
			if err != nil {
				t.Fatalf("SetupTracing(endpoint): %v", err)
			}
			_, span := otel.Tracer("flick-test").Start(ctx, "export-test")
			span.End()
			if err := shutdown(ctx); err != nil {
				t.Fatalf("shutdown: %v", err)
			}
			select {
			case path := <-paths:
				if path != tc.want {
					t.Fatalf("export path = %q, want %q", path, tc.want)
				}
			default:
				t.Fatal("no OTLP export received")
			}
		})
	}
}

func TestSetupTracingRejectsMalformedEndpoint(t *testing.T) {
	shutdown, err := SetupTracing(context.Background(), TracingOptions{
		ServiceName: "flick-test",
		Endpoint:    "http://collector.invalid/%zz?token=private-test-token",
	})
	if err == nil || err.Error() != "invalid otlp trace endpoint URL" {
		t.Fatalf("malformed endpoint error = %v, want sanitized parse error", err)
	}
	if shutdown != nil {
		t.Fatal("malformed endpoint returned a shutdown function")
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
