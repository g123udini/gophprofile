package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"

	"gophprofile/internal/events"
)

type AvatarUploadedHandler func(ctx context.Context, evt events.AvatarUploadedEvent) error

type Consumer struct {
	conn  *amqp.Connection
	ch    *amqp.Channel
	queue string
}

func NewConsumer(url, exchange, queue, routingKey string) (*Consumer, error) {
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

	return &Consumer{conn: conn, ch: ch, queue: queue}, nil
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
		slog.Error("decode delivery", "err", err, "message_id", msg.MessageId)
		_ = msg.Nack(false, false)
		return
	}

	if err := handler(ctx, evt); err != nil {
		slog.Error("handle avatar.uploaded",
			"err", err, "avatar_id", evt.AvatarID, "message_id", msg.MessageId)
		_ = msg.Nack(false, false)
		return
	}

	if err := msg.Ack(false); err != nil {
		slog.Error("ack delivery", "err", err, "message_id", msg.MessageId)
	}
}
