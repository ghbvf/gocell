package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time interface check.
var _ outbox.Subscriber = (*Subscriber)(nil)

// SubscriberConfig configures how a Subscriber consumes messages.
type SubscriberConfig struct {
	// QueueName is the queue to consume from. If empty, defaults to the topic name.
	QueueName string

	// PrefetchCount limits the number of unacknowledged messages per consumer.
	// Default: 10.
	PrefetchCount int

	// ShutdownTimeout is how long to wait for in-flight messages during Close().
	// Default: 30s.
	ShutdownTimeout time.Duration
}

func (sc *SubscriberConfig) setDefaults() {
	if sc.PrefetchCount <= 0 {
		sc.PrefetchCount = 10
	}
	if sc.ShutdownTimeout == 0 {
		sc.ShutdownTimeout = 30 * time.Second
	}
}

// Subscriber implements outbox.Subscriber using RabbitMQ.
//
// ref: Watermill watermill-amqp subscriber.go — reconnect loop + ACK/NACK pattern
// Adopted: per-subscription channel, QoS prefetch, graceful shutdown with WaitGroup.
// Deviated: callback-based handler (not channel-based) to align with GoCell ConsumerBase.
type Subscriber struct {
	conn   *Connection
	config SubscriberConfig

	mu       sync.Mutex
	closed   atomic.Bool
	closeCh  chan struct{}
	wg       sync.WaitGroup
	channels []AMQPChannel
}

// NewSubscriber creates a Subscriber with the given connection and config.
func NewSubscriber(conn *Connection, config SubscriberConfig) *Subscriber {
	config.setDefaults()
	return &Subscriber{
		conn:    conn,
		config:  config,
		closeCh: make(chan struct{}),
	}
}

// Subscribe registers a handler for the given topic and blocks until ctx is
// cancelled, the subscriber is closed, or an unrecoverable error occurs.
//
// The topic is used as a fanout exchange name. A queue (from SubscriberConfig
// or defaulting to the topic) is declared and bound to the exchange.
//
// Consumer: cg-{QueueName}-{topic}
// Idempotency key: handled by ConsumerBase (not in Subscriber)
// ACK timing: after handler returns nil
// Retry: transient errors -> NACK+requeue / permanent errors -> handled by ConsumerBase DLQ
func (s *Subscriber) Subscribe(ctx context.Context, topic string, handler func(context.Context, outbox.Entry) error) error {
	if s.closed.Load() {
		return errcode.New(ErrAdapterAMQPSubscribe, "rabbitmq: subscriber is closed")
	}

	queueName := s.config.QueueName
	if queueName == "" {
		queueName = topic
	}

	ch, err := s.conn.AcquireChannel()
	if err != nil {
		return errcode.Wrap(ErrAdapterAMQPSubscribe, "rabbitmq: acquire channel for subscribe", err)
	}

	s.mu.Lock()
	s.channels = append(s.channels, ch)
	s.mu.Unlock()

	// Set QoS.
	if err := ch.Qos(s.config.PrefetchCount, 0, false); err != nil {
		return errcode.Wrap(ErrAdapterAMQPSubscribe, "rabbitmq: set qos", err)
	}

	// Declare exchange idempotently.
	if err := ch.ExchangeDeclare(topic, "fanout", true, false, false, false, nil); err != nil {
		return errcode.Wrap(ErrAdapterAMQPSubscribe, "rabbitmq: declare exchange", err)
	}

	// Declare queue.
	if _, err = ch.QueueDeclare(queueName, true, false, false, false, nil); err != nil {
		return errcode.Wrap(ErrAdapterAMQPSubscribe, "rabbitmq: declare queue", err)
	}

	// Bind queue to exchange.
	if err := ch.QueueBind(queueName, "", topic, false, nil); err != nil {
		return errcode.Wrap(ErrAdapterAMQPSubscribe, "rabbitmq: bind queue", err)
	}

	consumerTag := fmt.Sprintf("cg-%s-%s", queueName, topic)

	deliveries, err := ch.Consume(queueName, consumerTag, false, false, false, false, nil)
	if err != nil {
		return errcode.Wrap(ErrAdapterAMQPConsume, "rabbitmq: start consuming", err)
	}

	slog.Info("rabbitmq: subscriber started",
		slog.String("topic", topic),
		slog.String("queue", queueName),
		slog.String("consumer", consumerTag),
		slog.Int("prefetch", s.config.PrefetchCount))

	return s.consumeLoop(ctx, ch, deliveries, topic, handler)
}

func (s *Subscriber) consumeLoop(
	ctx context.Context,
	ch AMQPChannel,
	deliveries <-chan amqp.Delivery,
	topic string,
	handler func(context.Context, outbox.Entry) error,
) error {
	for {
		select {
		case <-ctx.Done():
			slog.Info("rabbitmq: subscriber context cancelled",
				slog.String("topic", topic))
			return nil

		case <-s.closeCh:
			slog.Info("rabbitmq: subscriber closing",
				slog.String("topic", topic))
			return nil

		case delivery, ok := <-deliveries:
			if !ok {
				slog.Warn("rabbitmq: delivery channel closed, subscriber exiting",
					slog.String("topic", topic))
				return errcode.New(ErrAdapterAMQPConsume, "rabbitmq: delivery channel closed")
			}

			s.wg.Add(1)
			s.processDelivery(ctx, ch, delivery, topic, handler)
		}
	}
}

func (s *Subscriber) processDelivery(
	ctx context.Context,
	ch AMQPChannel,
	delivery amqp.Delivery,
	topic string,
	handler func(context.Context, outbox.Entry) error,
) {
	defer s.wg.Done()

	var entry outbox.Entry
	if err := json.Unmarshal(delivery.Body, &entry); err != nil {
		// Unmarshal failure is a permanent error — NACK without requeue.
		slog.Error("rabbitmq: unmarshal delivery failed, nacking without requeue",
			slog.String("topic", topic),
			slog.Uint64("delivery_tag", delivery.DeliveryTag),
			slog.String("error", err.Error()))
		if nackErr := ch.Nack(delivery.DeliveryTag, false, false); nackErr != nil {
			slog.Error("rabbitmq: nack failed",
				slog.String("topic", topic),
				slog.String("error", nackErr.Error()))
		}
		return
	}

	// Populate metadata from AMQP headers if present and entry metadata is empty.
	if entry.Metadata == nil {
		entry.Metadata = make(map[string]string)
	}
	entry.Metadata["topic"] = topic

	if err := handler(ctx, entry); err != nil {
		// If the context is cancelled (shutdown), NACK without requeue to avoid
		// requeue storm. The message will be picked up by another consumer or
		// redelivered after the consumer reconnects.
		if ctx.Err() != nil {
			slog.Info("rabbitmq: context cancelled during handler, nacking without requeue",
				slog.String("topic", topic),
				slog.String("event_id", entry.ID),
				slog.String("error", err.Error()))
			if nackErr := ch.Nack(delivery.DeliveryTag, false, false); nackErr != nil {
				slog.Error("rabbitmq: nack failed",
					slog.String("topic", topic),
					slog.String("error", nackErr.Error()))
			}
			return
		}

		// Handler error is a transient failure — NACK with requeue.
		slog.Warn("rabbitmq: handler returned error, nacking with requeue",
			slog.String("topic", topic),
			slog.String("event_id", entry.ID),
			slog.String("error", err.Error()))
		if nackErr := ch.Nack(delivery.DeliveryTag, false, true); nackErr != nil {
			slog.Error("rabbitmq: nack failed",
				slog.String("topic", topic),
				slog.String("error", nackErr.Error()))
		}
		return
	}

	// Handler succeeded — ACK.
	if err := ch.Ack(delivery.DeliveryTag, false); err != nil {
		slog.Error("rabbitmq: ack failed",
			slog.String("topic", topic),
			slog.String("event_id", entry.ID),
			slog.String("error", err.Error()))
	}
}

// Close terminates all active subscriptions and waits for in-flight messages.
func (s *Subscriber) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(s.closeCh)

	// Wait for in-flight messages with timeout.
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("rabbitmq: subscriber closed gracefully")
	case <-time.After(s.config.ShutdownTimeout):
		slog.Warn("rabbitmq: subscriber shutdown timed out",
			slog.Duration("timeout", s.config.ShutdownTimeout))
	}

	// Close all channels.
	s.mu.Lock()
	channels := s.channels
	s.channels = nil
	s.mu.Unlock()

	for _, ch := range channels {
		if err := ch.Close(); err != nil {
			slog.Debug("rabbitmq: error closing subscriber channel",
				slog.String("error", err.Error()))
		}
	}

	return nil
}
