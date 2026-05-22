package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace"
)

func TestNewLoggerTagsServiceAndVersion(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, "server", "1.2.3")

	logger.Info("hello")

	rec := decode(t, buf)
	require.Equal(t, "server", rec["service"])
	require.Equal(t, "1.2.3", rec["version"])
	require.Equal(t, "hello", rec["msg"])
}

func TestContextHandlerInjectsRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, "server", "dev")

	ctx := WithRequestID(context.Background(), "req-abc")
	logger.InfoContext(ctx, "served")

	rec := decode(t, buf)
	require.Equal(t, "req-abc", rec["request_id"])
}

func TestContextHandlerInjectsMessageID(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, "worker", "dev")

	ctx := WithMessageID(context.Background(), "msg-42")
	logger.InfoContext(ctx, "consumed")

	rec := decode(t, buf)
	require.Equal(t, "msg-42", rec["message_id"])
}

func TestContextHandlerOmitsAttrsWhenNotSet(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, "server", "dev")

	logger.InfoContext(context.Background(), "no ctx attrs")

	rec := decode(t, buf)
	_, hasReq := rec["request_id"]
	_, hasMsg := rec["message_id"]
	require.False(t, hasReq)
	require.False(t, hasMsg)
}

func TestWithRequestIDIgnoresEmpty(t *testing.T) {
	ctx := WithRequestID(context.Background(), "")
	require.Empty(t, RequestIDFromContext(ctx))
}

func TestContextHandlerInjectsTraceFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, "server", "dev")

	tp := trace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	logger.InfoContext(ctx, "with span")

	rec := decode(t, buf)
	require.Equal(t, span.SpanContext().TraceID().String(), rec["trace_id"])
	require.Equal(t, span.SpanContext().SpanID().String(), rec["span_id"])
}

func decode(t *testing.T, buf bytes.Buffer) map[string]any {
	t.Helper()
	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	return rec
}
