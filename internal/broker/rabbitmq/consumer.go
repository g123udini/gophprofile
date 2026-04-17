package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"gophprofile/internal/events"
)

const (
	defaultMaxRetryAttempts    = 3
	defaultInitialRetryBackoff = 500 * time.Millisecond
)

type AvatarUploadedHandler func(ctx context.Context, evt events.AvatarUploadedEvent) error

type Consumer struct {
	conn                *amqp.Connection
	ch                  *amqp.Channel
	queue               string
	maxRetryAttempts    int
	initialRetryBackoff time.Duration
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

func NewConsumer(url, exchange, queue, routingKey string, opts ...ConsumerOption) (*Consumer, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("rabbitmq: open channel: %w", err)
	}

	if err := ch.ExchangeDeclare(exchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("rabbitmq: declare exchange: %w", err)
	}

	if _, err := ch.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("rabbitmq: declare queue: %w", err)
	}

	if err := ch.QueueBind(queue, routingKey, exchange, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("rabbitmq: bind queue: %w", err)
	}

	if err := ch.Qos(1, 0, false); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("rabbitmq: qos: %w", err)
	}

	c := &Consumer{
		conn:                conn,
		ch:                  ch,
		queue:               queue,
		maxRetryAttempts:    defaultMaxRetryAttempts,
		initialRetryBackoff: defaultInitialRetryBackoff,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

func (c *Consumer) Close() error {
	_ = c.ch.Close()
	return c.conn.Close()
}

func (c *Consumer) Healthy() bool {
	return !c.conn.IsClosed()
}

// Run blocks until ctx is canceled or the channel closes. Each delivery is
// decoded and passed to the handler; success acks, error nacks without requeue.
func (c *Consumer) Run(ctx context.Context, handler AvatarUploadedHandler) error {
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
				return fmt.Errorf("rabbitmq: delivery channel closed")
			}
			c.handleDelivery(ctx, handler, msg)
		}
	}
}

func (c *Consumer) handleDelivery(ctx context.Context, handler AvatarUploadedHandler, msg amqp.Delivery) {
	var evt events.AvatarUploadedEvent
	if err := json.Unmarshal(msg.Body, &evt); err != nil {
		// Malformed payload is a poison message — retrying won't help.
		slog.Error("decode delivery, dropping", "err", err, "message_id", msg.MessageId)
		_ = msg.Nack(false, false)
		return
	}

	var lastErr error
	for attempt := 1; attempt <= c.maxRetryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			_ = msg.Nack(false, true) // shutting down — let the broker redeliver later
			return
		}

		if err := handler(ctx, evt); err == nil {
			if ackErr := msg.Ack(false); ackErr != nil {
				slog.Error("ack delivery", "err", ackErr, "message_id", msg.MessageId)
			}
			return
		} else {
			lastErr = err
		}

		if attempt == c.maxRetryAttempts {
			break
		}
		backoff := c.initialRetryBackoff * (1 << (attempt - 1))
		slog.Warn("handler failed, retrying",
			"err", lastErr, "attempt", attempt, "backoff", backoff,
			"avatar_id", evt.AvatarID, "message_id", msg.MessageId)
		select {
		case <-ctx.Done():
			_ = msg.Nack(false, true)
			return
		case <-time.After(backoff):
		}
	}

	slog.Error("handler exhausted retries, dropping",
		"err", lastErr, "attempts", c.maxRetryAttempts,
		"avatar_id", evt.AvatarID, "message_id", msg.MessageId)
	_ = msg.Nack(false, false)
}
