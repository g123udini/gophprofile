// Package logging wires the application's structured logger.
//
// New returns a JSON slog.Logger tagged with service and version, wrapped in a
// handler that copies request_id and message_id from the context onto every
// record produced via slog.*Context functions. The same logger is installed as
// slog.Default so package-level slog calls behave consistently.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
)

type contextKey string

const (
	requestIDKey contextKey = "request_id"
	messageIDKey contextKey = "message_id"
)

// New builds the default JSON logger and installs it as slog.Default.
func New(service, version string) *slog.Logger {
	return NewWithWriter(os.Stdout, service, version)
}

// NewWithWriter is the same as New but writes to the given writer (used in tests).
func NewWithWriter(w io.Writer, service, version string) *slog.Logger {
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(&contextHandler{Handler: base}).With(
		slog.String("service", service),
		slog.String("version", version),
	)
	slog.SetDefault(logger)
	return logger
}

// Version returns APP_VERSION or "dev" if unset. Useful from main entry points.
func Version() string {
	if v := os.Getenv("APP_VERSION"); v != "" {
		return v
	}
	return "dev"
}

// contextHandler enriches records with ctx-scoped attributes. Only slog.*Context
// calls reach Handle with a real ctx — package-level slog.Info etc. fall through
// to the underlying handler unchanged.
type contextHandler struct {
	slog.Handler
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx != nil {
		if v, ok := ctx.Value(requestIDKey).(string); ok && v != "" {
			r.AddAttrs(slog.String("request_id", v))
		}
		if v, ok := ctx.Value(messageIDKey).(string); ok && v != "" {
			r.AddAttrs(slog.String("message_id", v))
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}

// WithRequestID returns ctx tagged with the given request_id. A blank id is a no-op.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the request_id stored on ctx, or "" if none.
func RequestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey).(string)
	return v
}

// WithMessageID returns ctx tagged with the given message_id. A blank id is a no-op.
func WithMessageID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, messageIDKey, id)
}

// MessageIDFromContext returns the message_id stored on ctx, or "" if none.
func MessageIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(messageIDKey).(string)
	return v
}
