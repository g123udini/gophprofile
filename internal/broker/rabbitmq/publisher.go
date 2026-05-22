package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"gophprofile/internal/events"
)

const tracerName = "gophprofile/rabbitmq"

type Publisher struct {
	conn     *amqp.Connection
	ch       *amqp.Channel
	exchange string
	mu       sync.Mutex
	tracer   trace.Tracer
}

// amqpHeaderCarrier adapts amqp.Table to the OTel TextMapCarrier interface so
// the global propagator can inject/extract traceparent and baggage on AMQP
// messages.
type amqpHeaderCarrier amqp.Table

func (c amqpHeaderCarrier) Get(key string) string {
	v, ok := c[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func (c amqpHeaderCarrier) Set(key, value string) { c[key] = value }

func (c amqpHeaderCarrier) Keys() []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	return out
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
	return &Publisher{conn: conn, ch: ch, exchange: exchange, tracer: otel.Tracer(tracerName)}, nil
}

func (p *Publisher) Close() error {
	_ = p.ch.Close()
	return p.conn.Close()
}

func (p *Publisher) Healthy() bool {
	return !p.conn.IsClosed()
}

func (p *Publisher) PublishAvatarUploaded(ctx context.Context, evt events.AvatarUploadedEvent) (err error) {
	ctx, span := p.tracer.Start(ctx, "rabbitmq.publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.destination", p.exchange),
			attribute.String("messaging.rabbitmq.routing_key", events.RoutingKeyAvatarUploaded),
			attribute.String("messaging.message_id", evt.AvatarID),
		),
	)
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("rabbitmq: marshal event: %w", err)
	}

	headers := amqp.Table{}
	otel.GetTextMapPropagator().Inject(ctx, amqpHeaderCarrier(headers))

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
			Headers:      headers,
			Body:         body,
		},
	); err != nil {
		return fmt.Errorf("rabbitmq: publish %q: %w", events.RoutingKeyAvatarUploaded, err)
	}
	return nil
}

// extractContext rebuilds an OTel ctx from message headers using the global
// propagator. Used by Consumer.handleDelivery; lives next to the carrier.
func extractContext(ctx context.Context, headers amqp.Table) context.Context {
	if headers == nil {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, amqpHeaderCarrier(headers))
}
