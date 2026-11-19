package telemetry

import (
	"context"
	"fmt"
	"net/url"
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

// A URL without a path uses /v1/traces; explicit paths, including /, are preserved.
type TracingOptions struct {
	ServiceName string
	Endpoint    string
}

// Export errors are async and go to OTel's error handler, so a bad collector never crashes the app.
func SetupTracing(ctx context.Context, opts TracingOptions) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if opts.Endpoint == "" {
		return noop, nil
	}

	endpoint, err := url.Parse(opts.Endpoint)
	if err != nil {
		// The parse error includes the URL, which may contain collector credentials.
		return nil, fmt.Errorf("invalid otlp trace endpoint URL")
	}
	// Preserve the pre-v1.45 exporter default for URLs without a path.
	if endpoint.Path == "" {
		endpoint.Path = "/v1/traces"
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint.String()))
	if err != nil {
		return nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}

	// NewSchemaless avoids coupling to a semconv version.
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
		// ParentBased honors an upstream sampling decision across api->worker propagation.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return provider.Shutdown, nil
}

// Callers must not wrap secret content into the errors they pass.
func EndSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

func serviceVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "dev"
}
