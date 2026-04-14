package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"gophprofile/internal/events"
)

type Publisher struct {
	conn     *amqp.Connection
	ch       *amqp.Channel
	exchange string
	mu       sync.Mutex
}

func NewPublisher(url, exchange string) (*Publisher, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("rabbitmq: open channel: %w", err)
	}
	if err := ch.ExchangeDeclare(
		exchange,
		amqp.ExchangeTopic,
		true,  // durable
		false, // auto-delete
		false, // internal
		false, // no-wait
		nil,
	); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("rabbitmq: declare exchange %q: %w", exchange, err)
	}
	return &Publisher{conn: conn, ch: ch, exchange: exchange}, nil
}

func (p *Publisher) Close() error {
	_ = p.ch.Close()
	return p.conn.Close()
}

func (p *Publisher) Healthy() bool {
	return !p.conn.IsClosed()
}

func (p *Publisher) PublishAvatarUploaded(ctx context.Context, evt events.AvatarUploadedEvent) error {
	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("rabbitmq: marshal event: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ch.PublishWithContext(
		ctx,
		p.exchange,
		events.RoutingKeyAvatarUploaded,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    evt.AvatarID,
			Timestamp:    time.Now().UTC(),
			Body:         body,
		},
	); err != nil {
		return fmt.Errorf("rabbitmq: publish %q: %w", events.RoutingKeyAvatarUploaded, err)
	}
	return nil
}
