package rabbitmq_test

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
	tcrabbit "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"

	"gophprofile/internal/broker/rabbitmq"
	"gophprofile/internal/events"
)

const testExchange = "avatars.test"

var testAmqpURL string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ctr, err := tcrabbit.Run(ctx, "rabbitmq:3-management-alpine")
	if err != nil {
		log.Printf("start rabbitmq container: %v", err)
		return 1
	}
	defer func() { _ = ctr.Terminate(context.Background()) }()

	testAmqpURL, err = ctr.AmqpURL(ctx)
	if err != nil {
		log.Printf("amqp url: %v", err)
		return 1
	}
	return m.Run()
}

func newPublisher(t *testing.T) *rabbitmq.Publisher {
	t.Helper()
	p, err := rabbitmq.NewPublisher(testAmqpURL, testExchange)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// bindTestQueue creates an ephemeral queue bound to the test exchange with the
// given routing key and returns a delivery channel the test can drain.
func bindTestQueue(t *testing.T, routingKey string) <-chan amqp.Delivery {
	t.Helper()
	conn, err := amqp.Dial(testAmqpURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ch, err := conn.Channel()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ch.Close() })

	require.NoError(t, ch.ExchangeDeclare(
		testExchange, amqp.ExchangeTopic, true, false, false, false, nil,
	))

	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	require.NoError(t, err)

	require.NoError(t, ch.QueueBind(q.Name, routingKey, testExchange, false, nil))

	deliveries, err := ch.Consume(q.Name, "", true, false, false, false, nil)
	require.NoError(t, err)
	return deliveries
}

func TestPublisher_NewPublisher_DeclareExchangeIdempotent(t *testing.T) {
	p1, err := rabbitmq.NewPublisher(testAmqpURL, testExchange)
	require.NoError(t, err)
	defer p1.Close()

	p2, err := rabbitmq.NewPublisher(testAmqpURL, testExchange)
	require.NoError(t, err, "second declare on same exchange must succeed")
	defer p2.Close()
}

func TestPublisher_PublishAvatarUploaded_DeliversMessage(t *testing.T) {
	deliveries := bindTestQueue(t, events.RoutingKeyAvatarUploaded)
	p := newPublisher(t)

	evt := events.AvatarUploadedEvent{
		AvatarID: "11111111-2222-3333-4444-555555555555",
		UserID:   "user-1",
		S3Key:    "avatars/user-1/1.jpg",
	}
	require.NoError(t, p.PublishAvatarUploaded(context.Background(), evt))

	select {
	case msg := <-deliveries:
		require.Equal(t, events.RoutingKeyAvatarUploaded, msg.RoutingKey)
		require.Equal(t, "application/json", msg.ContentType)
		require.Equal(t, evt.AvatarID, msg.MessageId)
		require.Equal(t, amqp.Persistent, msg.DeliveryMode)

		var got events.AvatarUploadedEvent
		require.NoError(t, json.Unmarshal(msg.Body, &got))
		require.Equal(t, evt, got)
	case <-time.After(3 * time.Second):
		t.Fatal("no delivery received within 3s")
	}
}

func TestPublisher_Healthy_ReportsOpenConnection(t *testing.T) {
	p := newPublisher(t)
	require.True(t, p.Healthy())
}

func TestConsumer_Run_DeliversToHandlerAndAcks(t *testing.T) {
	c, err := rabbitmq.NewConsumer(testAmqpURL, testExchange, "test-consumer-happy", events.RoutingKeyAvatarUploaded)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	received := make(chan events.AvatarUploadedEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, func(_ context.Context, evt events.AvatarUploadedEvent) error {
			received <- evt
			return nil
		})
	}()

	p := newPublisher(t)
	require.NoError(t, p.PublishAvatarUploaded(context.Background(), events.AvatarUploadedEvent{
		AvatarID: "consumer-test", UserID: "u", S3Key: "k",
	}))

	select {
	case evt := <-received:
		require.Equal(t, "consumer-test", evt.AvatarID)
	case <-time.After(3 * time.Second):
		t.Fatal("handler not called within 3s")
	}

	cancel()
	<-done
	require.True(t, c.Healthy(), "connection must still be healthy after ctx cancel")
}

func TestConsumer_Run_RetriesOnErrorThenGivesUp(t *testing.T) {
	c, err := rabbitmq.NewConsumer(
		testAmqpURL, testExchange, "test-consumer-error",
		events.RoutingKeyAvatarUploaded,
		rabbitmq.WithRetry(3, 10*time.Millisecond),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	var attempts atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = c.Run(ctx, func(_ context.Context, _ events.AvatarUploadedEvent) error {
			attempts.Add(1)
			return errTest
		})
	}()

	p := newPublisher(t)
	require.NoError(t, p.PublishAvatarUploaded(context.Background(), events.AvatarUploadedEvent{
		AvatarID: "err", UserID: "u", S3Key: "k",
	}))

	require.Eventually(t,
		func() bool { return attempts.Load() == 3 },
		2*time.Second, 20*time.Millisecond,
		"handler must be retried up to maxAttempts")

	cancel()
}

func TestConsumer_Run_RetriesOnErrorThenAcks(t *testing.T) {
	c, err := rabbitmq.NewConsumer(
		testAmqpURL, testExchange, "test-consumer-retry-ok",
		events.RoutingKeyAvatarUploaded,
		rabbitmq.WithRetry(3, 10*time.Millisecond),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	var attempts atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = c.Run(ctx, func(_ context.Context, _ events.AvatarUploadedEvent) error {
			n := attempts.Add(1)
			if n < 3 {
				return errTest
			}
			return nil
		})
	}()

	p := newPublisher(t)
	require.NoError(t, p.PublishAvatarUploaded(context.Background(), events.AvatarUploadedEvent{
		AvatarID: "retry-ok", UserID: "u", S3Key: "k",
	}))

	require.Eventually(t,
		func() bool { return attempts.Load() == 3 },
		2*time.Second, 20*time.Millisecond,
		"handler must succeed on third attempt")

	// Give the acker a moment; if the message were nacked-with-requeue, attempts
	// would keep climbing. Assert it stays at 3.
	time.Sleep(200 * time.Millisecond)
	require.EqualValues(t, 3, attempts.Load(), "successful attempt must ack, not requeue")

	cancel()
}

var errTest = errorString("handler failed")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestConsumer_Run_ReconnectsAfterChannelClose(t *testing.T) {
	c, err := rabbitmq.NewConsumer(
		testAmqpURL, testExchange, "test-consumer-reconnect",
		events.RoutingKeyAvatarUploaded,
		rabbitmq.WithReconnectBackoff(100*time.Millisecond),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	received := make(chan string, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, func(_ context.Context, evt events.AvatarUploadedEvent) error {
			received <- evt.AvatarID
			return nil
		})
	}()

	p := newPublisher(t)
	require.NoError(t, p.PublishAvatarUploaded(context.Background(), events.AvatarUploadedEvent{
		AvatarID: "before-reconnect", UserID: "u", S3Key: "k",
	}))
	waitForID(t, received, "before-reconnect", 3*time.Second)

	// Give Ack a moment to flush so the broker doesn't redeliver "before-reconnect".
	time.Sleep(100 * time.Millisecond)

	require.NoError(t, c.ForceReconnect())

	require.Eventually(t, c.Healthy, 3*time.Second, 50*time.Millisecond,
		"consumer must come back healthy after reconnect")

	require.NoError(t, p.PublishAvatarUploaded(context.Background(), events.AvatarUploadedEvent{
		AvatarID: "after-reconnect", UserID: "u", S3Key: "k",
	}))
	waitForID(t, received, "after-reconnect", 3*time.Second)

	cancel()
	<-done
}

func waitForID(t *testing.T, c <-chan string, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case got := <-c:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("did not receive %q within %s", want, timeout)
		}
	}
}

func TestPublisher_PropagatesTraceparentInHeaders(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tp := trace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	otel.SetTracerProvider(tp)

	deliveries := bindTestQueue(t, events.RoutingKeyAvatarUploaded)
	p := newPublisher(t)

	ctx, span := tp.Tracer("test").Start(context.Background(), "publish")
	wantTraceID := span.SpanContext().TraceID().String()

	require.NoError(t, p.PublishAvatarUploaded(ctx, events.AvatarUploadedEvent{
		AvatarID: "trace-test", UserID: "u", S3Key: "k",
	}))
	span.End()

	select {
	case msg := <-deliveries:
		tp, ok := msg.Headers["traceparent"].(string)
		require.True(t, ok, "traceparent header missing on delivery")
		// W3C traceparent format: "00-<trace_id>-<span_id>-<flags>"
		require.Contains(t, tp, wantTraceID)
	case <-time.After(3 * time.Second):
		t.Fatal("no delivery received within 3s")
	}
}

func TestPublisher_DropsMessagesWhenRoutingKeyDoesNotMatchAnyBinding(t *testing.T) {
	// Queue bound to "avatar.uploaded" but publisher currently only emits that
	// routing key. This test asserts topic-routing semantics: publishing the
	// same key delivers, publishing another routing key does not.
	deliveries := bindTestQueue(t, "some.other.key")
	p := newPublisher(t)

	require.NoError(t, p.PublishAvatarUploaded(context.Background(), events.AvatarUploadedEvent{
		AvatarID: "x", UserID: "u", S3Key: "k",
	}))

	select {
	case msg := <-deliveries:
		t.Fatalf("unexpected delivery: routing key %q", msg.RoutingKey)
	case <-time.After(500 * time.Millisecond):
		// expected: no delivery
	}
}
