package tracing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestInit_EmptyEndpointReturnsNoopShutdown(t *testing.T) {
	shutdown, err := Init(context.Background(), "test-svc", "v0", "", true)
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	require.NoError(t, shutdown(context.Background()))
}

func TestInit_InstallsCompositePropagator(t *testing.T) {
	_, err := Init(context.Background(), "test-svc", "v0", "", true)
	require.NoError(t, err)

	// Build a valid SpanContext directly so the test doesn't need an sdk
	// TracerProvider — the only thing under test here is the propagator.
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:     trace.SpanID{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	// TraceContext propagator emits "traceparent". A no-op default propagator
	// would leave the carrier empty.
	_, has := carrier["traceparent"]
	require.True(t, has, "expected TraceContext propagator to inject traceparent")
}
