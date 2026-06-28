// tracing.go wires OpenTelemetry distributed tracing for the api and worker
// processes. Tracing is OFF by default: SetupTracing installs a real tracer
// provider only when an OTLP endpoint is configured, so a deployment without a
// collector pays nothing — the global provider stays OTel's no-op and every
// tracer.Start is a cheap no-op. Spans must never carry secret content
// (plaintext, passphrases, derived keys, ciphertext, KDF salts); see
// docs/architecture/security-model.md and this package's AGENTS.md.
package telemetry

import (
	"context"
	"fmt"
	"runtime/debug"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// TracingOptions configures SetupTracing. Endpoint is FLICK_OTLP_ENDPOINT, a
// full OTLP/HTTP URL such as "http://otel-collector:4318"; empty disables
// tracing entirely.
type TracingOptions struct {
	ServiceName string
	Endpoint    string
}

// SetupTracing installs a global OTLP/HTTP tracer provider for opts.ServiceName
// and returns a shutdown function the caller must invoke on exit to flush
// buffered spans. When opts.Endpoint is empty it installs nothing and returns a
// no-op shutdown, so tracing stays off with zero overhead and no collector
// dependency. A malformed endpoint URL fails fast here; export/connection
// errors are async and handled by OTel's internal error handler (a bad
// collector never crashes the app).
func SetupTracing(ctx context.Context, opts TracingOptions) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if opts.Endpoint == "" {
		return noop, nil
	}

	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(opts.Endpoint))
	if err != nil {
		return nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}

	// NewSchemaless avoids coupling to a specific semconv version; "service.name"
	// is the well-known resource key every collector recognizes. Merging onto
	// resource.Default() keeps the SDK's process/runtime attributes.
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", opts.ServiceName),
		attribute.String("service.version", serviceVersion()),
	))
	if err != nil {
		return nil, fmt.Errorf("build trace resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		// ParentBased(AlwaysSample) samples every root span and honors an
		// upstream sampling decision once cross-process propagation lands
		// (#133 PR2). A ratio sampler is deferred until trace volume warrants it.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)
	otel.SetTracerProvider(provider)
	// W3C trace-context + baggage so a span can cross the api↔worker boundary;
	// injected/extracted via the job payload in internal/events/propagation.go.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return provider.Shutdown, nil
}

// EndSpan records err on span (when non-nil) and ends it, centralizing the
// record-error-then-end idiom. Traced operations declare a named-return error
// and `defer func() { telemetry.EndSpan(span, err) }()` so every error path is
// captured. Callers must not wrap secret content into the errors they return.
func EndSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// serviceVersion reports the build's module version (set when built as a
// versioned module), falling back to "dev" for local/unversioned builds.
func serviceVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "dev"
}
