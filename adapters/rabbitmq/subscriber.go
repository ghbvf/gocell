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
	// ErrAdapterAMQPConnect or ErrAdapterAMQPReconnecting from AcquireChannel /
	// Health means the connection is nil, IsClosed, or mid-reconnect — transient.
	var ecErr *errcode.Error
	if errors.As(err, &ecErr) && (ecErr.Code == ErrAdapterAMQPConnect || ecErr.Code == ErrAdapterAMQPReconnecting) {
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

// Compile-time interface checks.
var (
	_ outbox.Subscriber            = (*Subscriber)(nil)
	_ outbox.SubscriberInitializer = (*Subscriber)(nil)
)

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

// resolveQueueName derives the queue name from config and runtime parameters.
// Priority (highest to lowest):
//  1. config.QueueName (explicit static override)
//  2. runtime consumerGroup + topic (e.g. "audit-core.session.created")
//  3. config.ConsumerGroup + topic (from SubscriberConfig)
//  4. topic name as-is (backward compatible fallback)
func (s *Subscriber) resolveQueueName(topic, consumerGroup string) string {
	if s.config.QueueName != "" {
		return s.config.QueueName
	}
	if consumerGroup != "" {
		return consumerGroup + "." + topic
	}
	if s.config.ConsumerGroup != "" {
		return s.config.ConsumerGroup + "." + topic
	}
	return topic
}

// declareTopology declares the exchange, DLX, queue, and binding on the given
// channel. All operations are idempotent — safe to call multiple times.
func (s *Subscriber) declareTopology(ch AMQPChannel, topic, queueName string) error {
	// Declare exchange idempotently.
	if err := ch.ExchangeDeclare(topic, "fanout", true, false, false, false, nil); err != nil {
		return fmt.Errorf("rabbitmq: declare exchange: %w", err)
	}

	// Declare the dead-letter exchange to ensure it exists before binding.
	// Uses "direct" type so rejected messages are routed by DLXRoutingKey.
	if err := ch.ExchangeDeclare(s.config.DLXExchange, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("rabbitmq: declare DLX exchange: %w", err)
	}

	// Build queue arguments for dead-letter routing.
	queueArgs := amqp.Table{
		"x-dead-letter-exchange": s.config.DLXExchange,
	}
	if s.config.DLXRoutingKey != "" {
		queueArgs["x-dead-letter-routing-key"] = s.config.DLXRoutingKey
	}

	// Declare queue.
	if _, err := ch.QueueDeclare(queueName, true, false, false, false, queueArgs); err != nil {
		return fmt.Errorf("rabbitmq: declare queue: %w", err)
	}

	// Bind queue to exchange.
	if err := ch.QueueBind(queueName, "", topic, false, nil); err != nil {
		return fmt.Errorf("rabbitmq: bind queue: %w", err)
	}

	return nil
}

// InitializeSubscription pre-declares the AMQP topology (exchange, DLX, queue,
// binding) for the given topic and consumer group. After this returns, messages
// published to the topic are queued by the broker — even before Subscribe
// starts consuming. This enables deterministic conformance testing without sleep.
//
// ref: Watermill message.SubscribeInitializer — synchronous topology pre-creation.
func (s *Subscriber) InitializeSubscription(ctx context.Context, topic, consumerGroup string) error {
	if s.config.DLXExchange == "" {
		return errcode.New(ErrAdapterAMQPSubscribe,
			"rabbitmq: DLXExchange is required for InitializeSubscription")
	}

	ch, err := s.conn.AcquireChannel()
	if err != nil {
		return fmt.Errorf("rabbitmq: acquire channel for init: %w", err)
	}
	defer s.conn.ReleaseChannel(ch)

	queueName := s.resolveQueueName(topic, consumerGroup)
	return s.declareTopology(ch, topic, queueName)
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
func (s *Subscriber) Subscribe(ctx context.Context, topic string, handler outbox.EntryHandler, consumerGroup string) error {
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

	queueName := s.resolveQueueName(topic, consumerGroup)

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
			slog.String(logKeyTopic, topic),
			slog.String("queue", queueName),
			slog.String("error", err.Error()))

		// Wait for connection recovery before re-subscribing.
		if waitErr := s.conn.WaitConnected(subCtx); waitErr != nil {
			// Terminal connection error — propagate to caller so upstream
			// (EventRouter/Bootstrap) can detect and handle terminal failure.
			// Covers both ErrAdapterAMQPConnectPermanent (bad credentials)
			// and ErrAdapterAMQPReconnectExhausted (max attempts exceeded).
			if isTerminalConnectionError(waitErr) {
				return waitErr
			}
			// ctx cancelled or subscriber closed during wait — clean exit.
			return nil
		}

		slog.Info("rabbitmq: resubscribing after reconnect",
			slog.String(logKeyTopic, topic),
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
		// Terminal state — propagate immediately, do not wrap as subscribe error.
		if isTerminalConnectionError(err) {
			return err
		}
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
		if err := ch.Close(); err != nil {
			slog.Debug("rabbitmq: error closing channel during cleanup",
				slog.String("error", err.Error()))
		}
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

	// Declare topology (exchange, DLX, queue, binding) — idempotent.
	if err := s.declareTopology(ch, topic, queueName); err != nil {
		return setupErr("rabbitmq: declare topology", ErrAdapterAMQPSubscribe, err)
	}

	consumerTag := fmt.Sprintf("cg-%s-%s", queueName, topic)

	deliveries, err := ch.Consume(queueName, consumerTag, false, false, false, false, nil)
	if err != nil {
		return setupErr("rabbitmq: start consuming", ErrAdapterAMQPConsume, err)
	}

	slog.Info("rabbitmq: subscriber started",
		slog.String(logKeyTopic, topic),
		slog.String("queue", queueName),
		slog.String("consumer", consumerTag),
		slog.Int("prefetch", s.config.PrefetchCount))

	loopErr := s.consumeLoop(ctx, ch, deliveries, topic, handler)

	// Clean up the dead channel after consumeLoop exits.
	s.untrackChannel(ch)
	if closeErr := ch.Close(); closeErr != nil {
		slog.Debug("rabbitmq: error closing consumed channel",
			slog.String("error", closeErr.Error()))
	}

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
				slog.String(logKeyTopic, topic))
			return nil

		case <-s.closeCh:
			slog.Info("rabbitmq: subscriber closing",
				slog.String(logKeyTopic, topic))
			return nil

		case delivery, ok := <-deliveries:
			if !ok {
				slog.Warn("rabbitmq: delivery channel closed, subscriber exiting",
					slog.String(logKeyTopic, topic))
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

	entry, err := unmarshalDelivery(delivery.Body)
	if err != nil {
		// Unmarshal failure is a permanent error — NACK without requeue.
		slog.Error("rabbitmq: unmarshal delivery failed, nacking without requeue",
			slog.String(logKeyTopic, topic),
			slog.Uint64("delivery_tag", delivery.DeliveryTag),
			slog.String("error", err.Error()))
		if nackErr := ch.Nack(delivery.DeliveryTag, false, false); nackErr != nil {
			slog.Error("rabbitmq: nack failed",
				slog.String(logKeyTopic, topic),
				slog.String("error", nackErr.Error()))
		}
		return
	}

	// Populate metadata from AMQP headers if present and entry metadata is empty.
	if entry.Metadata == nil {
		entry.Metadata = make(map[string]string)
	}
	entry.Metadata["topic"] = topic

	// Observability metadata (request_id, correlation_id, trace_id) is restored
	// into the handler context by ObservabilityContextMiddleware, not here.
	// The middleware is registered by bootstrap (or manually via SubscriberWithMiddleware).
	// This separation keeps the subscriber adapter transport-only and moves the
	// observability concern to the composable middleware layer.
	deliveryCtx := ctx

	// Solution B: handler returns HandleResult with explicit Disposition + Receipt.
	res := handler(deliveryCtx, entry)

	// Execute broker-level disposition.
	var brokerErr error
	switch res.Disposition {
	case outbox.DispositionAck:
		brokerErr = ch.Ack(delivery.DeliveryTag, false)
		if brokerErr != nil {
			logAttrsWithContext(deliveryCtx, slog.LevelError, "rabbitmq: ack failed",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyEventID, entry.ID),
				slog.String("error", brokerErr.Error()))
		}
	case outbox.DispositionReject:
		brokerErr = ch.Nack(delivery.DeliveryTag, false, false)
		if brokerErr != nil {
			logAttrsWithContext(deliveryCtx, slog.LevelError, "rabbitmq: nack(reject) failed",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyEventID, entry.ID),
				slog.String("error", brokerErr.Error()))
		}
	case outbox.DispositionRequeue:
		brokerErr = ch.Nack(delivery.DeliveryTag, false, true)
		if brokerErr != nil {
			logAttrsWithContext(deliveryCtx, slog.LevelError, "rabbitmq: nack(requeue) failed",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyEventID, entry.ID),
				slog.String("error", brokerErr.Error()))
		}
	default:
		logAttrsWithContext(deliveryCtx, slog.LevelError, "rabbitmq: unknown disposition, nacking with requeue",
			slog.String(logKeyTopic, topic),
			slog.String(logKeyEventID, entry.ID),
			slog.String("disposition", res.Disposition.String()))
		brokerErr = ch.Nack(delivery.DeliveryTag, false, true)
	}

	// Log handler-level error if present (separate from broker error).
	if res.Err != nil {
		logAttrsWithContext(deliveryCtx, slog.LevelWarn, "rabbitmq: handler reported error",
			slog.String(logKeyTopic, topic),
			slog.String(logKeyEventID, entry.ID),
			slog.String("disposition", res.Disposition.String()),
			slog.String("error", res.Err.Error()))
	}

	s.settleReceipt(deliveryCtx, res, topic, entry.ID, brokerErr)
}

// settleReceipt commits or releases the idempotency receipt after the broker
// Ack/Nack outcome is known. Uses a detached context with a 5s timeout so
// the operation completes even during graceful shutdown.
func (s *Subscriber) settleReceipt(
	ctx context.Context,
	res outbox.HandleResult,
	topic, eventID string,
	brokerErr error,
) {
	if res.Receipt == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if brokerErr != nil {
		if relErr := res.Receipt.Release(rctx); relErr != nil {
			logAttrsWithContext(rctx, slog.LevelError, "rabbitmq: receipt release failed after broker error",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyEventID, eventID),
				slog.String("error", relErr.Error()))
		}
		return
	}

	switch res.Disposition {
	case outbox.DispositionAck:
		if err := res.Receipt.Commit(rctx); err != nil {
			logAttrsWithContext(rctx, slog.LevelError, "rabbitmq: receipt commit failed",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyEventID, eventID),
				slog.String("error", err.Error()))
		}
	default:
		// Reject/Requeue/unknown — release so DLQ replay or redelivery can re-enter.
		if err := res.Receipt.Release(rctx); err != nil {
			logAttrsWithContext(rctx, slog.LevelError, "rabbitmq: receipt release failed",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyEventID, eventID),
				slog.String("error", err.Error()))
		}
	}
}

func logAttrsWithContext(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	slog.LogAttrs(ctx, level, msg, attrs...)
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

// ---------------------------------------------------------------------------
// Wire format deserialization
// ---------------------------------------------------------------------------

// outboxWireMessage is the wire envelope produced by the three-phase relay.
// Fields use camelCase JSON tags.
//
// NOTE: adapters/postgres/outbox_relay.go defines an identical outboxMessage
// for serialization — keep the two structs in sync when modifying fields.
type outboxWireMessage struct {
	ID            string            `json:"id"`
	AggregateID   string            `json:"aggregateId,omitempty"`
	AggregateType string            `json:"aggregateType,omitempty"`
	EventType     string            `json:"eventType"`
	Topic         string            `json:"topic,omitempty"`
	Payload       json.RawMessage   `json:"payload"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
}

// unmarshalDelivery deserializes a broker message body into an outbox.Entry.
// It first tries the new outboxWireMessage envelope, then falls back to the
// legacy full outbox.Entry format for backward compatibility.
//
// Discriminator: In the new wire format, payload is embedded JSON (starts
// with '{' or '[' as json.RawMessage). In legacy format, outbox.Entry.Payload
// is []byte which json.Marshal encodes as base64 (starts with '"'). Go's
// json.Unmarshal does case-insensitive key matching, so we cannot rely on
// PascalCase vs camelCase to distinguish formats — we must check the payload
// shape instead.
func unmarshalDelivery(body []byte) (outbox.Entry, error) {
	var msg outboxWireMessage
	if err := json.Unmarshal(body, &msg); err == nil && msg.ID != "" && msg.EventType != "" && isEmbeddedJSON(msg.Payload) {
		return outbox.Entry{
			ID:            msg.ID,
			AggregateID:   msg.AggregateID,
			AggregateType: msg.AggregateType,
			EventType:     msg.EventType,
			Topic:         msg.Topic,
			Payload:       []byte(msg.Payload),
			Metadata:      msg.Metadata,
			CreatedAt:     msg.CreatedAt,
		}, nil
	}

	// Fallback: legacy full Entry (PascalCase, Payload is base64-encoded []byte).
	var entry outbox.Entry
	if err := json.Unmarshal(body, &entry); err != nil {
		return outbox.Entry{}, fmt.Errorf("unmarshal delivery: %w", err)
	}
	return entry, nil
}

// isEmbeddedJSON returns true if the raw JSON value is an object or array
// (new wire format), as opposed to a base64 string (legacy format).
func isEmbeddedJSON(raw json.RawMessage) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}
