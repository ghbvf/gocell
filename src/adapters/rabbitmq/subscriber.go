package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// errSubscriptionLost is a sentinel error returned by subscribeOnce when the
// delivery channel is closed (broker restart, network partition). The outer
// Subscribe loop only reconnects on this error; all other errors (topology,
// permissions) are returned to the caller immediately.
var errSubscriptionLost = errors.New("rabbitmq: subscription lost")

// isRecoverableAMQPError returns true if the error indicates a transient
// connection/channel loss that can be recovered via reconnect. Permanent errors
// (ACCESS_REFUSED, PRECONDITION_FAILED, channel_max exhausted) return false.
func isRecoverableAMQPError(err error) bool {
	if err == nil {
		return false
	}
	// amqp.ErrClosed: connection or channel was closed.
	if errors.Is(err, amqp.ErrClosed) {
		return true
	}
	// ErrAdapterAMQPConnect from AcquireChannel means the connection is nil or
	// IsClosed — this is transient and should trigger reconnect.
	var ecErr *errcode.Error
	if errors.As(err, &ecErr) && ecErr.Code == ErrAdapterAMQPConnect {
		return true
	}
	// AMQP protocol errors: Recover=true means the broker will restart the
	// channel; Recover=false (ACCESS_REFUSED, PRECONDITION_FAILED) is permanent.
	var amqpErr *amqp.Error
	if errors.As(err, &amqpErr) {
		return amqpErr.Recover
	}
	return false
}

// Compile-time interface check.
var _ outbox.Subscriber = (*Subscriber)(nil)

// SubscriberConfig configures how a Subscriber consumes messages.
type SubscriberConfig struct {
	// QueueName is the queue to consume from. If set, it takes precedence over
	// ConsumerGroup-based naming. If both QueueName and ConsumerGroup are empty,
	// the queue name defaults to the topic name (backward compatible).
	QueueName string

	// ConsumerGroup identifies the logical consumer group. When QueueName is empty
	// and ConsumerGroup is set, the queue name is derived as "{ConsumerGroup}.{topic}".
	// This ensures that multiple cells subscribing to the same fanout exchange each
	// get their own queue (fanout semantics) instead of competing on a single queue.
	ConsumerGroup string

	// DLXExchange is the dead-letter exchange name. When set, the queue is declared
	// with x-dead-letter-exchange so that NACK(requeue=false) messages are routed
	// to the DLX instead of being silently discarded by the broker.
	DLXExchange string

	// DLXRoutingKey is an optional routing key for dead-lettered messages.
	// Only effective when DLXExchange is set.
	DLXRoutingKey string

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

// resolveQueueName derives the queue name from config and topic.
// Priority: QueueName > ConsumerGroup.topic > topic (backward compat).
func (s *Subscriber) resolveQueueName(topic string) string {
	if s.config.QueueName != "" {
		return s.config.QueueName
	}
	if s.config.ConsumerGroup != "" {
		return s.config.ConsumerGroup + "." + topic
	}
	return topic
}

// Subscribe registers a handler for the given topic and blocks until ctx is
// cancelled or the subscriber is closed.
//
// Subscribe automatically reconnects when the underlying AMQP channel is lost
// (e.g., due to a broker restart or network partition). It waits for the
// Connection to re-establish via WaitConnected, then re-declares the exchange,
// queue, and binding on a fresh channel.
//
// The topic is used as a fanout exchange name. A queue (from SubscriberConfig
// or defaulting to the topic) is declared and bound to the exchange.
//
// Consumer: cg-{QueueName}-{topic}
// Idempotency key: handled by ConsumerBase middleware (not in Subscriber)
// ACK timing: after handler returns DispositionAck
// Retry: DispositionRequeue -> NACK+requeue / DispositionReject -> NACK(no-requeue) → DLX
func (s *Subscriber) Subscribe(ctx context.Context, topic string, handler outbox.EntryHandler) error {
	if s.closed.Load() {
		return errcode.New(ErrAdapterAMQPSubscribe, "rabbitmq: subscriber is closed")
	}
	if s.config.DLXExchange == "" {
		return errcode.New(ErrAdapterAMQPSubscribe,
			"rabbitmq: DLXExchange is required — without a dead-letter exchange, "+
				"Nack(requeue=false) silently discards messages. "+
				"Set SubscriberConfig.DLXExchange to a valid DLX name")
	}

	// Derive a context that is cancelled when either the parent ctx is done or
	// the subscriber is closed. This ensures WaitConnected unblocks promptly on
	// subscriber shutdown even if the parent ctx has no deadline.
	subCtx, subCancel := context.WithCancelCause(ctx)
	defer subCancel(nil)
	go func() {
		select {
		case <-s.closeCh:
			subCancel(fmt.Errorf("subscriber closed"))
		case <-subCtx.Done():
		}
	}()

	queueName := s.resolveQueueName(topic)

	for {
		err := s.subscribeOnce(subCtx, topic, queueName, handler)
		if err == nil {
			// Clean exit: ctx cancelled or subscriber closed.
			return nil
		}

		// Only reconnect on delivery channel lost. Topology/permission errors
		// (ExchangeDeclare, QueueDeclare, QueueBind) are permanent — return
		// immediately so the caller can handle them.
		if !errors.Is(err, errSubscriptionLost) {
			return err
		}

		// Check if we should stop retrying.
		select {
		case <-subCtx.Done():
			return nil
		default:
		}
		if s.closed.Load() {
			return nil
		}

		slog.Warn("rabbitmq: subscription lost, waiting for reconnect",
			slog.String("topic", topic),
			slog.String("queue", queueName),
			slog.String("error", err.Error()))

		// Wait for connection recovery before re-subscribing.
		if waitErr := s.conn.WaitConnected(subCtx); waitErr != nil {
			// ctx cancelled or subscriber closed during wait — clean exit.
			return nil
		}

		slog.Info("rabbitmq: resubscribing after reconnect",
			slog.String("topic", topic),
			slog.String("queue", queueName))
	}
}

// subscribeOnce performs a single subscription lifecycle: acquire channel,
// declare topology, consume, and run the consume loop.
//
// Returns nil for a clean exit (ctx cancelled or subscriber closed).
// Returns a non-nil error when the delivery channel is lost (triggers reconnect
// in the outer Subscribe loop).
func (s *Subscriber) subscribeOnce(
	ctx context.Context,
	topic, queueName string,
	handler outbox.EntryHandler,
) error {
	ch, err := s.conn.AcquireChannel()
	if err != nil {
		if isRecoverableAMQPError(err) {
			return fmt.Errorf("%w: acquire channel: %v", errSubscriptionLost, err)
		}
		return errcode.Wrap(ErrAdapterAMQPSubscribe, "rabbitmq: acquire channel for subscribe", err)
	}

	s.trackChannel(ch)

	// cleanupCh closes and untracks the channel. Used on early-return error
	// paths to prevent channel leaks.
	cleanupCh := func() {
		s.untrackChannel(ch)
		_ = ch.Close()
	}

	// setupErr wraps a setup-stage error. If the underlying AMQP error is
	// recoverable (connection/channel closed mid-setup), it wraps as
	// errSubscriptionLost so the outer loop can reconnect and re-run the
	// full setup. Otherwise it returns a permanent error to the caller.
	setupErr := func(msg string, code errcode.Code, err error) error {
		cleanupCh()
		if isRecoverableAMQPError(err) {
			return fmt.Errorf("%w: %s: %v", errSubscriptionLost, msg, err)
		}
		return errcode.Wrap(code, msg, err)
	}

	// Set QoS.
	if err := ch.Qos(s.config.PrefetchCount, 0, false); err != nil {
		return setupErr("rabbitmq: set qos", ErrAdapterAMQPSubscribe, err)
	}

	// Declare exchange idempotently.
	if err := ch.ExchangeDeclare(topic, "fanout", true, false, false, false, nil); err != nil {
		return setupErr("rabbitmq: declare exchange", ErrAdapterAMQPSubscribe, err)
	}

	// Build queue arguments for dead-letter routing.
	// DLXExchange is guaranteed non-empty (validated in Subscribe).
	queueArgs := amqp.Table{
		"x-dead-letter-exchange": s.config.DLXExchange,
	}
	if s.config.DLXRoutingKey != "" {
		queueArgs["x-dead-letter-routing-key"] = s.config.DLXRoutingKey
	}

	// Declare queue.
	if _, err = ch.QueueDeclare(queueName, true, false, false, false, queueArgs); err != nil {
		return setupErr("rabbitmq: declare queue", ErrAdapterAMQPSubscribe, err)
	}

	// Bind queue to exchange.
	if err := ch.QueueBind(queueName, "", topic, false, nil); err != nil {
		return setupErr("rabbitmq: bind queue", ErrAdapterAMQPSubscribe, err)
	}

	consumerTag := fmt.Sprintf("cg-%s-%s", queueName, topic)

	deliveries, err := ch.Consume(queueName, consumerTag, false, false, false, false, nil)
	if err != nil {
		return setupErr("rabbitmq: start consuming", ErrAdapterAMQPConsume, err)
	}

	slog.Info("rabbitmq: subscriber started",
		slog.String("topic", topic),
		slog.String("queue", queueName),
		slog.String("consumer", consumerTag),
		slog.Int("prefetch", s.config.PrefetchCount))

	loopErr := s.consumeLoop(ctx, ch, deliveries, topic, handler)

	// Clean up the dead channel after consumeLoop exits.
	s.untrackChannel(ch)
	_ = ch.Close() // Best-effort close; channel is likely already dead.

	return loopErr
}

// trackChannel adds a channel to the tracked list for cleanup on Close().
func (s *Subscriber) trackChannel(ch AMQPChannel) {
	s.mu.Lock()
	s.channels = append(s.channels, ch)
	s.mu.Unlock()
}

// untrackChannel removes a channel from the tracked list.
func (s *Subscriber) untrackChannel(ch AMQPChannel) {
	s.mu.Lock()
	for i, tracked := range s.channels {
		if tracked == ch {
			s.channels = append(s.channels[:i], s.channels[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
}

func (s *Subscriber) consumeLoop(
	ctx context.Context,
	ch AMQPChannel,
	deliveries <-chan amqp.Delivery,
	topic string,
	handler outbox.EntryHandler,
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
				return fmt.Errorf("%w: delivery channel closed", errSubscriptionLost)
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
	handler outbox.EntryHandler,
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

	// Solution B: handler returns HandleResult with explicit Disposition + Receipt.
	res := handler(ctx, entry)

	// Execute broker-level disposition.
	var brokerErr error
	switch res.Disposition {
	case outbox.DispositionAck:
		brokerErr = ch.Ack(delivery.DeliveryTag, false)
		if brokerErr != nil {
			slog.Error("rabbitmq: ack failed",
				slog.String("topic", topic),
				slog.String("event_id", entry.ID),
				slog.String("error", brokerErr.Error()))
		}
	case outbox.DispositionReject:
		brokerErr = ch.Nack(delivery.DeliveryTag, false, false)
		if brokerErr != nil {
			slog.Error("rabbitmq: nack(reject) failed",
				slog.String("topic", topic),
				slog.String("event_id", entry.ID),
				slog.String("error", brokerErr.Error()))
		}
	case outbox.DispositionRequeue:
		brokerErr = ch.Nack(delivery.DeliveryTag, false, true)
		if brokerErr != nil {
			slog.Error("rabbitmq: nack(requeue) failed",
				slog.String("topic", topic),
				slog.String("event_id", entry.ID),
				slog.String("error", brokerErr.Error()))
		}
	default:
		slog.Error("rabbitmq: unknown disposition, nacking with requeue",
			slog.String("topic", topic),
			slog.String("event_id", entry.ID),
			slog.String("disposition", res.Disposition.String()))
		brokerErr = ch.Nack(delivery.DeliveryTag, false, true)
	}

	// Log handler-level error if present (separate from broker error).
	if res.Err != nil {
		slog.Warn("rabbitmq: handler reported error",
			slog.String("topic", topic),
			slog.String("event_id", entry.ID),
			slog.String("disposition", res.Disposition.String()),
			slog.String("error", res.Err.Error()))
	}

	// Commit or release the idempotency receipt based on broker outcome.
	// Use WithoutCancel + timeout: broker Ack/Nack already succeeded,
	// idempotency state must be persisted even during graceful shutdown,
	// but must not block indefinitely on network partitions.
	if res.Receipt == nil {
		return
	}
	receiptCtx, receiptCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer receiptCancel()
	if brokerErr != nil {
		// Broker disposition failed — release so redelivery can re-enter.
		if relErr := res.Receipt.Release(receiptCtx); relErr != nil {
			slog.Error("rabbitmq: receipt release failed after broker error",
				slog.String("topic", topic),
				slog.String("event_id", entry.ID),
				slog.String("error", relErr.Error()))
		}
		return
	}
	switch res.Disposition {
	case outbox.DispositionAck:
		if commitErr := res.Receipt.Commit(receiptCtx); commitErr != nil {
			slog.Error("rabbitmq: receipt commit failed",
				slog.String("topic", topic),
				slog.String("event_id", entry.ID),
				slog.String("error", commitErr.Error()))
		}
	case outbox.DispositionReject, outbox.DispositionRequeue:
		// Reject releases (not commits) so DLQ replay can reprocess.
		if relErr := res.Receipt.Release(receiptCtx); relErr != nil {
			slog.Error("rabbitmq: receipt release failed",
				slog.String("topic", topic),
				slog.String("event_id", entry.ID),
				slog.String("error", relErr.Error()))
		}
	default:
		if relErr := res.Receipt.Release(receiptCtx); relErr != nil {
			slog.Error("rabbitmq: receipt release failed (unknown disposition)",
				slog.String("topic", topic),
				slog.String("event_id", entry.ID),
				slog.String("error", relErr.Error()))
		}
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
