package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"gophprofile/internal/events"
	"gophprofile/internal/logging"
)

const (
	defaultMaxRetryAttempts    = 3
	defaultInitialRetryBackoff = 500 * time.Millisecond
	defaultReconnectBackoff    = 2 * time.Second
)

type AvatarUploadedHandler func(ctx context.Context, evt events.AvatarUploadedEvent) error

type Consumer struct {
	url        string
	exchange   string
	queue      string
	routingKey string

	conn *amqp.Connection
	ch   *amqp.Channel

	maxRetryAttempts    int
	initialRetryBackoff time.Duration
	reconnectBackoff    time.Duration

	tracer trace.Tracer
}

type ConsumerOption func(*Consumer)

// WithRetry overrides the default exponential-backoff retry policy. maxAttempts
// is the total number of handler invocations per message (including the first);
// initialBackoff is the pause before the first retry and doubles on each
// subsequent attempt.
func WithRetry(maxAttempts int, initialBackoff time.Duration) ConsumerOption {
	return func(c *Consumer) {
		c.maxRetryAttempts = maxAttempts
		c.initialRetryBackoff = initialBackoff
	}
}

// WithReconnectBackoff overrides the pause between reconnect attempts when
// Run loses the underlying AMQP connection.
func WithReconnectBackoff(backoff time.Duration) ConsumerOption {
	return func(c *Consumer) {
		c.reconnectBackoff = backoff
	}
}

func NewConsumer(url, exchange, queue, routingKey string, opts ...ConsumerOption) (*Consumer, error) {
	c := &Consumer{
		url:                 url,
		exchange:            exchange,
		queue:               queue,
		routingKey:          routingKey,
		maxRetryAttempts:    defaultMaxRetryAttempts,
		initialRetryBackoff: defaultInitialRetryBackoff,
		reconnectBackoff:    defaultReconnectBackoff,
		tracer:              otel.Tracer(tracerName),
	}
	for _, opt := range opts {
		opt(c)
	}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

// connect opens a fresh AMQP connection, channel, and redeclares the exchange,
// queue, and binding. Safe to call for initial setup or reconnect: any existing
// conn/ch is closed first.
func (c *Consumer) connect() error {
	if c.ch != nil {
		_ = c.ch.Close()
		c.ch = nil
	}
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}

	conn, err := amqp.Dial(c.url)
	if err != nil {
		return fmt.Errorf("rabbitmq: dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("rabbitmq: open channel: %w", err)
	}
	if err := ch.ExchangeDeclare(c.exchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return fmt.Errorf("rabbitmq: declare exchange: %w", err)
	}
	if _, err := ch.QueueDeclare(c.queue, true, false, false, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return fmt.Errorf("rabbitmq: declare queue: %w", err)
	}
	if err := ch.QueueBind(c.queue, c.routingKey, c.exchange, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return fmt.Errorf("rabbitmq: bind queue: %w", err)
	}
	if err := ch.Qos(1, 0, false); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return fmt.Errorf("rabbitmq: qos: %w", err)
	}
	c.conn = conn
	c.ch = ch
	return nil
}

// ForceReconnect closes the current AMQP channel so that Run observes the
// disconnect and reconnects. Useful for maintenance and tests.
func (c *Consumer) ForceReconnect() error {
	if c.ch == nil {
		return nil
	}
	return c.ch.Close()
}

func (c *Consumer) Close() error {
	if c.ch != nil {
		_ = c.ch.Close()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *Consumer) Healthy() bool {
	return c.conn != nil && !c.conn.IsClosed()
}

// Run blocks until ctx is canceled. If the AMQP connection drops, Run
// transparently reconnects with backoff and resumes consumption.
func (c *Consumer) Run(ctx context.Context, handler AvatarUploadedHandler) error {
	for {
		err := c.consumeLoop(ctx, handler)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.WarnContext(ctx, "rabbitmq consumer disconnected, reconnecting",
			"err", err, "backoff", c.reconnectBackoff)
		if reconnectErr := c.reconnectWithBackoff(ctx); reconnectErr != nil {
			return reconnectErr
		}
		slog.InfoContext(ctx, "rabbitmq consumer reconnected")
	}
}

var errDeliveriesClosed = errors.New("rabbitmq: delivery channel closed")

func (c *Consumer) consumeLoop(ctx context.Context, handler AvatarUploadedHandler) error {
	deliveries, err := c.ch.Consume(c.queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("rabbitmq: consume: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-deliveries:
			if !ok {
				return errDeliveriesClosed
			}
			c.handleDelivery(ctx, handler, msg)
		}
	}
}

func (c *Consumer) reconnectWithBackoff(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := c.connect(); err == nil {
			return nil
		} else {
			slog.WarnContext(ctx, "rabbitmq reconnect attempt failed",
				"err", err, "backoff", c.reconnectBackoff)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.reconnectBackoff):
		}
	}
}

func (c *Consumer) handleDelivery(ctx context.Context, handler AvatarUploadedHandler, msg amqp.Delivery) {
	// Restore the upstream trace from AMQP headers before opening our own span,
	// so the consumer span links to the producer's span across services.
	ctx = extractContext(ctx, msg.Headers)
	ctx, span := c.tracer.Start(ctx, "rabbitmq.consume",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.source", c.queue),
			attribute.String("messaging.rabbitmq.routing_key", c.routingKey),
			attribute.String("messaging.message_id", msg.MessageId),
		),
	)
	defer span.End()

	msgCtx := logging.WithMessageID(ctx, msg.MessageId)

	var evt events.AvatarUploadedEvent
	if err := json.Unmarshal(msg.Body, &evt); err != nil {
		// Malformed payload is a poison message — retrying won't help.
		span.RecordError(err)
		span.SetStatus(codes.Error, "decode failed")
		slog.ErrorContext(msgCtx, "decode delivery, dropping", "err", err)
		_ = msg.Nack(false, false)
		return
	}

	var lastErr error
	for attempt := 1; attempt <= c.maxRetryAttempts; attempt++ {
		if err := msgCtx.Err(); err != nil {
			_ = msg.Nack(false, true) // shutting down — let the broker redeliver later
			return
		}

		if err := handler(msgCtx, evt); err == nil {
			if ackErr := msg.Ack(false); ackErr != nil {
				slog.ErrorContext(msgCtx, "ack delivery", "err", ackErr)
			}
			return
		} else {
			lastErr = err
		}

		if attempt == c.maxRetryAttempts {
			break
		}
		backoff := c.initialRetryBackoff * (1 << (attempt - 1))
		slog.WarnContext(msgCtx, "handler failed, retrying",
			"err", lastErr, "attempt", attempt, "backoff", backoff,
			"avatar_id", evt.AvatarID)
		select {
		case <-msgCtx.Done():
			_ = msg.Nack(false, true)
			return
		case <-time.After(backoff):
		}
	}

	span.RecordError(lastErr)
	span.SetStatus(codes.Error, "exhausted retries")
	slog.ErrorContext(msgCtx, "handler exhausted retries, dropping",
		"err", lastErr, "attempts", c.maxRetryAttempts,
		"avatar_id", evt.AvatarID)
	_ = msg.Nack(false, false)
}
