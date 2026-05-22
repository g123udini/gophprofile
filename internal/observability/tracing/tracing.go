// Package tracing wires the OpenTelemetry trace SDK against an OTLP gRPC
// exporter (the project's otel-collector). Init returns a shutdown func that
// must be deferred from main; if the configured endpoint is empty, it returns
// a no-op shutdown and leaves the global TracerProvider as the default no-op,
// so callers can run the binary without an observability stack.
package tracing

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// ShutdownFunc flushes any buffered spans and tears the provider down. Always
// safe to call, even if Init found no endpoint configured.
type ShutdownFunc func(context.Context) error

// Init installs a global TracerProvider that exports OTLP traces over gRPC to
// endpoint, and a TraceContext+Baggage propagator. Endpoint is host:port (no
// scheme) — typically "otel-collector:4317" in compose, "localhost:4317"
// locally. An empty endpoint installs no-op shutdown and leaves global
// providers untouched.
func Init(ctx context.Context, serviceName, version, endpoint string, insecure bool) (ShutdownFunc, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
	if insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	exp, err := otlptrace.New(ctx, otlptracegrpc.NewClient(opts...))
	if err != nil {
		return nil, fmt.Errorf("tracing: otlp exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(version),
	))
	if err != nil {
		return nil, fmt.Errorf("tracing: build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
		// AlwaysSample: this is a learning project; cost is irrelevant and a
		// missing span on a demo request is more confusing than the volume.
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}
