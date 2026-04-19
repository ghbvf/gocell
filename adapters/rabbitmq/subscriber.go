package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	outboxrt "github.com/ghbvf/gocell/runtime/outbox"
)

// errSubscriptionLost is a sentinel error returned by subscribeOnce when the
// delivery channel is closed (broker restart, network partition). The outer
// Subscribe loop only reconnects on this error; all other errors (topology,
// permissions) are returned to the caller immediately.
var errSubscriptionLost = errors.New("rabbitmq: subscription lost")

// maxEntryIDLength is the maximum allowed byte length for entry.ID.
// Aligned with AMQP 0-9-1 shortstr limit (255 octets) to ensure the ID
// can be safely embedded in AMQP message headers without truncation.
const maxEntryIDLength = 255

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
	_ outbox.Subscriber              = (*Subscriber)(nil)
	_ outbox.SubscriberIntakeStopper = (*Subscriber)(nil)
	//nolint:staticcheck // SubscriberInitializer is deprecated but Subscriber implements it for backward compat.
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

	// StopIntakePerCallTimeout bounds any single basic.cancel call during
	// StopIntake. A hung broker cannot stall the whole shutdown chain beyond
	// this budget per consumer. Default: 2s.
	StopIntakePerCallTimeout time.Duration
}

func (sc *SubscriberConfig) setDefaults() {
	if sc.PrefetchCount <= 0 {
		sc.PrefetchCount = 10
	}
	if sc.StopIntakePerCallTimeout == 0 {
		sc.StopIntakePerCallTimeout = 2 * time.Second
	}
}

// Subscriber implements outbox.Subscriber using RabbitMQ.
//
// ref: Watermill watermill-amqp subscriber.go — reconnect loop + ACK/NACK pattern
// Adopted: per-subscription channel, QoS prefetch, graceful shutdown with WaitGroup.
// Deviated: callback-based handler (not channel-based) to align with GoCell ConsumerBase.
// Deviated: added StopIntake / drainRemaining to support two-phase shutdown
// (stopIntakeCh + basic.cancel before hard closeCh signal).
// Refactored (Part 3 T8): replaced s.channels + s.consumerTags with s.runs map of
// *subscriptionRun to fix A19 reconnect race (localWg.Wait before ch.Close).
//
// ref: nats-io/nats.go Subscription state encapsulation
// ref: uber-go/fx per-component lifecycle
type Subscriber struct {
	conn   *Connection
	config SubscriberConfig

	closed  atomic.Bool
	closeCh chan struct{}
	wg      sync.WaitGroup

	// runs tracks all active subscriptionRun instances (one per subscribeOnce
	// call). Protected by runsMu. Each run encapsulates its own AMQP channel,
	// consumer tag, and in-flight processDelivery WaitGroup (A19 fix).
	runsMu sync.Mutex
	runs   map[*subscriptionRun]struct{}

	// stopIntakeCh is closed by StopIntake to signal consumeLoop to enter
	// drain mode (process prefetched deliveries, accept no new ones).
	stopIntakeCh chan struct{}
	// stopIntakeOnce guards the single close of stopIntakeCh.
	stopIntakeOnce sync.Once
}

// NewSubscriber creates a Subscriber with the given connection and config.
func NewSubscriber(conn *Connection, config SubscriberConfig) *Subscriber {
	config.setDefaults()
	return &Subscriber{
		conn:         conn,
		config:       config,
		closeCh:      make(chan struct{}),
		stopIntakeCh: make(chan struct{}),
		runs:         make(map[*subscriptionRun]struct{}),
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
//
// Precondition: s.config.DLXExchange must be non-empty. Both call sites
// (Subscribe, InitializeSubscription) validate this, but the guard here
// prevents accidental misuse from future code paths.
func (s *Subscriber) declareTopology(ch AMQPChannel, topic, queueName string) error {
	if s.config.DLXExchange == "" {
		return fmt.Errorf("rabbitmq: declareTopology: DLXExchange must not be empty")
	}

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

// Setup implements outbox.Subscriber by pre-declaring AMQP topology (exchange,
// DLX, queue, binding) for the given subscription. After this returns, messages
// published to the topic are queued by the broker -- even before Subscribe
// starts consuming. This enables deterministic conformance testing without sleep.
//
// ref: Watermill message.SubscribeInitializer -- synchronous topology pre-creation.
func (s *Subscriber) Setup(ctx context.Context, sub outbox.Subscription) error {
	if s.config.DLXExchange == "" {
		return errcode.New(ErrAdapterAMQPSubscribe,
			"rabbitmq: DLXExchange is required for Setup")
	}

	ch, err := s.conn.AcquireChannel()
	if err != nil {
		return fmt.Errorf("rabbitmq: acquire channel for setup: %w", err)
	}
	defer s.conn.ReleaseChannel(ch)

	queueName := s.resolveQueueName(sub.Topic, sub.ConsumerGroup)
	return s.declareTopology(ch, sub.Topic, queueName)
}

// Ready implements outbox.Subscriber. RabbitMQ topology is declared synchronously
// in Setup; once Setup returns, the subscription is immediately ready. Returns an
// already-closed channel so callers do not block.
func (s *Subscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// InitializeSubscription implements outbox.SubscriberInitializer for backward
// compatibility. Delegates to Setup.
//
// Deprecated: callers should use Setup directly.
func (s *Subscriber) InitializeSubscription(ctx context.Context, topic, consumerGroup string) error {
	return s.Setup(ctx, outbox.Subscription{Topic: topic, ConsumerGroup: consumerGroup})
}

// Subscribe registers a handler for the given subscription and blocks until ctx
// is cancelled or the subscriber is closed.
//
// Subscribe automatically reconnects when the underlying AMQP channel is lost
// (e.g., due to a broker restart or network partition). It waits for the
// Connection to re-establish via WaitConnected, then re-declares the exchange,
// queue, and binding on a fresh channel.
//
// sub.Topic is used as a fanout exchange name. A queue (from SubscriberConfig
// or defaulting to the topic) is declared and bound to the exchange.
//
// Consumer: cg-{QueueName}-{sub.Topic}
// Idempotency key: handled by ConsumerBase middleware (not in Subscriber)
// ACK timing: after handler returns DispositionAck
// Retry: DispositionRequeue -> NACK+requeue / DispositionReject -> NACK(no-requeue) -> DLX
func (s *Subscriber) Subscribe(ctx context.Context, sub outbox.Subscription, handler outbox.EntryHandler) error {
	topic := sub.Topic
	consumerGroup := sub.ConsumerGroup
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
			return nil // Clean exit: ctx cancelled or subscriber closed.
		}
		// Only reconnect on delivery channel lost. Topology/permission errors
		// are permanent — return immediately.
		if !errors.Is(err, errSubscriptionLost) {
			return err
		}
		if reconnErr := s.awaitReconnect(subCtx, topic, queueName, err); reconnErr != nil {
			return reconnErr
		}
		// awaitReconnect returns nil both on successful reconnect AND on clean
		// exit (ctx cancelled / subscriber closed). Re-check before looping back
		// into subscribeOnce to avoid spinning when ctx is already done.
		select {
		case <-subCtx.Done():
			return nil
		default:
		}
		if s.closed.Load() {
			return nil
		}
	}
}

// awaitReconnect logs the subscription loss and waits for the connection to
// recover before the outer Subscribe loop retries subscribeOnce. Returns nil
// when the connection recovers (or clean exit), non-nil on terminal error or
// if the subscriber was stopped.
func (s *Subscriber) awaitReconnect(ctx context.Context, topic, queueName string, lostErr error) error {
	// Check if we should stop retrying before blocking on WaitConnected.
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	if s.closed.Load() {
		return nil
	}

	slog.Warn("rabbitmq: subscription lost, waiting for reconnect",
		slog.String(logKeyTopic, topic),
		slog.String("queue", queueName),
		slog.String("error", lostErr.Error()))

	if waitErr := s.conn.WaitConnected(ctx); waitErr != nil {
		// Terminal connection error — propagate so EventRouter/Bootstrap can handle.
		if isTerminalConnectionError(waitErr) {
			return waitErr
		}
		return nil // ctx cancelled or subscriber closed during wait.
	}

	slog.Info("rabbitmq: resubscribing after reconnect",
		slog.String(logKeyTopic, topic),
		slog.String("queue", queueName))
	return nil
}

// subscribeOnce performs a single subscription lifecycle: acquire channel,
// declare topology, consume, and run the consume loop.
//
// Returns nil for a clean exit (ctx cancelled or subscriber closed).
// Returns a non-nil error when the delivery channel is lost (triggers reconnect
// in the outer Subscribe loop).
//
// A19 fix: subscriptionRun encapsulates the per-subscribeOnce AMQP channel and
// its local WaitGroup. On reconnect (loopErr != nil), localWg.Wait() is called
// before ch.Close() so no processDelivery goroutine races against a closed channel.
//
// ref: rabbitmq/amqp091-go channel.go — Cancel→drain→wg.Wait→ch.Close ordering
// ref: nats-io/nats.go Subscription.Drain — per-subscription state encapsulation
func (s *Subscriber) subscribeOnce(
	ctx context.Context,
	topic, queueName string,
	handler outbox.EntryHandler,
) error {
	ch, err := s.conn.AcquireChannel()
	if err != nil {
		return classifyAcquireChannelError(err)
	}

	// setupErr wraps a setup-stage error. Closes and removes the run from
	// the tracked set. If the underlying AMQP error is recoverable, wraps as
	// errSubscriptionLost so the outer loop can reconnect and re-run setup.
	// Otherwise returns a permanent error.
	cleanupChannelDirect := func() {
		if closeErr := ch.Close(); closeErr != nil {
			slog.Debug("rabbitmq: error closing channel during cleanup",
				slog.String("error", closeErr.Error()))
		}
	}
	setupErr := func(msg string, code errcode.Code, err error) error {
		cleanupChannelDirect()
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
	// AMQP shortstr limit is 255 bytes. Truncate long tags to 250 bytes and
	// append a 4-byte CRC32 hex suffix to preserve uniqueness.
	const maxConsumerTagLen = 250
	if len(consumerTag) > maxConsumerTagLen {
		hash := fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(consumerTag)))
		consumerTag = consumerTag[:maxConsumerTagLen-9] + "-" + hash
		slog.Warn("rabbitmq: consumerTag truncated to fit AMQP shortstr limit",
			slog.String("consumer", consumerTag))
	}

	deliveries, err := ch.Consume(queueName, consumerTag, false, false, false, false, nil)
	if err != nil {
		return setupErr("rabbitmq: start consuming", ErrAdapterAMQPConsume, err)
	}

	// Create and track a subscriptionRun for this invocation.
	run := newSubscriptionRun(ch, consumerTag)
	s.addRun(run)
	defer s.removeRun(run)

	slog.Info("rabbitmq: subscriber started",
		slog.String(logKeyTopic, topic),
		slog.String("queue", queueName),
		slog.String("consumer", consumerTag),
		slog.Int("prefetch", s.config.PrefetchCount))

	loopErr := s.consumeLoop(ctx, run, deliveries, topic, handler)

	// A19 fix: wait for all in-flight processDelivery goroutines of THIS run
	// before closing the AMQP channel. This prevents Ack/Nack calls on a
	// closed channel (ErrClosed noise + potential redelivery).
	//
	// On reconnect (loopErr != nil): give a finite budget so the reconnect path
	// does not block indefinitely when the parent ctx has no deadline. 30s matches
	// the prior ShutdownTimeout default.
	//
	// On clean exit (loopErr == nil): Close() will also call waitAndClose via
	// the runs snapshot, but run.closed.Once ensures ch.Close is called exactly once.
	waitCtx := ctx
	if loopErr != nil {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			var cancelWait context.CancelFunc
			waitCtx, cancelWait = context.WithTimeout(context.Background(), 30*time.Second)
			defer cancelWait()
		}
	}
	_ = run.waitAndClose(waitCtx)

	return loopErr
}

// classifyAcquireChannelError maps AcquireChannel failures into the right
// error surface for the outer Subscribe loop: terminal → propagate;
// recoverable → errSubscriptionLost (triggers reconnect); other → wrap.
func classifyAcquireChannelError(err error) error {
	if isTerminalConnectionError(err) {
		return err
	}
	if isRecoverableAMQPError(err) {
		return fmt.Errorf("%w: acquire channel: %v", errSubscriptionLost, err)
	}
	return errcode.Wrap(ErrAdapterAMQPSubscribe, "rabbitmq: acquire channel for subscribe", err)
}

// addRun registers a subscriptionRun in the active runs map.
func (s *Subscriber) addRun(r *subscriptionRun) {
	s.runsMu.Lock()
	s.runs[r] = struct{}{}
	s.runsMu.Unlock()
}

// removeRun deregisters a subscriptionRun from the active runs map.
func (s *Subscriber) removeRun(r *subscriptionRun) {
	s.runsMu.Lock()
	delete(s.runs, r)
	s.runsMu.Unlock()
}

// snapshotActiveRuns returns a snapshot of all currently active subscriptionRuns
// without holding runsMu. Safe to iterate after returning.
func (s *Subscriber) snapshotActiveRuns() []*subscriptionRun {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	snapshot := make([]*subscriptionRun, 0, len(s.runs))
	for r := range s.runs {
		snapshot = append(snapshot, r)
	}
	return snapshot
}

// consumeLoop drains the deliveries channel and dispatches each delivery in a
// dedicated goroutine, honouring PrefetchCount as real concurrency.
//
// Concurrent safety audit:
//   - ch.Ack/Nack: guarded by amqp091-go's internal channel mutex; safe to call
//     from multiple goroutines simultaneously.
//     ref: rabbitmq/amqp091-go channel.go (sendMethod holds ch.m.Lock).
//   - Receipt: per-delivery local variable passed into processDelivery — no
//     sharing across goroutines.
//   - dispatchAck/releaseReceipt/dispatchDisposition: use only the per-delivery
//     ch, tag, and Receipt; no Subscriber-level mutable state.
//   - s.wg + run.localWg: sync.WaitGroup — concurrency-safe by design.
//   - topic/handler: immutable after Subscribe call.
//
// ref: ThreeDotsLabs/watermill message/router.go h.run — per-message goroutine
func (s *Subscriber) consumeLoop(
	ctx context.Context,
	run *subscriptionRun,
	deliveries <-chan amqp.Delivery,
	topic string,
	handler outbox.EntryHandler,
) error {
	ch := run.ch
	for {
		// Priority check: if StopIntake has fired, we MUST enter drainRemaining
		// even when closeCh or ctx.Done is also ready. This preserves the
		// "StopIntake → drain → Close" ordering invariant that callers rely on
		// — without it, a concurrent Close() can short-circuit to the closeCh
		// case here and silently drop prefetched deliveries.
		select {
		case <-s.stopIntakeCh:
			slog.Info("rabbitmq: intake stopped, entering drain mode",
				slog.String(logKeyTopic, topic))
			return s.drainRemaining(ctx, run, deliveries, topic, handler)
		default:
		}

		select {
		case <-s.stopIntakeCh:
			slog.Info("rabbitmq: intake stopped, entering drain mode",
				slog.String(logKeyTopic, topic))
			return s.drainRemaining(ctx, run, deliveries, topic, handler)

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
			run.registerDelivery()
			go func(d amqp.Delivery) {
				defer run.markDeliveryDone()
				s.processDelivery(ctx, ch, d, topic, handler)
			}(delivery)
		}
	}
}

// drainRemaining processes deliveries already prefetched after StopIntake
// issued basic.cancel. It exits when:
//   - the deliveries channel is closed (broker acknowledged basic.cancel), or
//   - ctx is cancelled (hard stop from parent), or
//   - closeCh fires (Close() was called as the hard-shutdown boundary).
//
// New deliveries should not arrive after basic.cancel; any that do (race
// window) are still processed correctly.
//
// ref: ThreeDotsLabs/watermill-amqp subscriber.go — closedChan drain pattern
// drainDeadline is the maximum wall-clock time drainRemaining waits for the
// deliveries channel to close after StopIntake issued basic.cancel. A healthy
// broker closes the chan within milliseconds of the cancel ack; this ceiling
// exists solely to prevent an indefinite hang when the broker is unresponsive.
var drainDeadline = 30 * time.Second

// drainRemaining reads prefetched deliveries until the deliveries channel is
// closed by the broker (the cancel ack path), the outer Subscribe ctx is
// cancelled (hard abort), or drainDeadline elapses (broker never ack'd the
// cancel).
//
// Invariant: closeCh is intentionally NOT in the select. Once StopIntake has
// entered the drain path, honouring closeCh here would race with buffered
// deliveries — the broker typically keeps pushing a handful of messages
// between the cancel and its ack, and the handler-processing gap makes the
// client-side buffer momentarily empty. We MUST let the broker drive the
// exit by closing the chan, otherwise prefetched-but-unacked messages are
// silently lost.
//
// ref: Watermill router.go — drain completes when source closes, not when
//
//	an external stop signal fires.
func (s *Subscriber) drainRemaining(
	ctx context.Context,
	run *subscriptionRun,
	deliveries <-chan amqp.Delivery,
	topic string,
	handler outbox.EntryHandler,
) error {
	ch := run.ch
	timer := time.NewTimer(drainDeadline)
	defer timer.Stop()

	for {
		select {
		case d, ok := <-deliveries:
			if !ok {
				slog.Info("rabbitmq: delivery channel closed after basic.cancel, drain complete",
					slog.String(logKeyTopic, topic))
				return nil
			}
			s.wg.Add(1)
			run.registerDelivery()
			go func(d amqp.Delivery) {
				defer run.markDeliveryDone()
				s.processDelivery(ctx, ch, d, topic, handler)
			}(d)
		case <-ctx.Done():
			slog.Info("rabbitmq: drain interrupted by context cancellation",
				slog.String(logKeyTopic, topic))
			return nil
		case <-timer.C:
			slog.Warn("rabbitmq: drain deadline reached, broker did not acknowledge basic.cancel",
				slog.String(logKeyTopic, topic),
				slog.Duration("deadline", drainDeadline))
			return nil
		}
	}
}

// nackPermanent calls ch.Nack(tag, false, false) and logs a warning if it fails.
// Used for permanent errors (unmarshal, invalid entry.ID) that must not be requeued.
func (s *Subscriber) nackPermanent(ch AMQPChannel, tag uint64, topic string) {
	if err := ch.Nack(tag, false, false); err != nil {
		slog.Error("rabbitmq: nack failed",
			slog.String(logKeyTopic, topic),
			slog.String("error", err.Error()))
	}
}

// validateEntryIDLength returns true if entry.ID exceeds the AMQP shortstr
// limit. Kept separate so that the too-long branch can log the truncated
// length for diagnostics; outbox.Entry.Validate covers the empty-ID case.
func validateEntryIDLength(id string) bool {
	return len(id) > maxEntryIDLength
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
		s.nackPermanent(ch, delivery.DeliveryTag, topic)
		return
	}

	// AMQP shortstr cap: too-long IDs cannot survive a broker round-trip, so
	// reject before touching metadata. Logged with capped length to indicate
	// overflow magnitude without exposing the full byte count.
	if validateEntryIDLength(entry.ID) {
		slog.LogAttrs(ctx, slog.LevelError, "rabbitmq: entry.ID exceeds AMQP shortstr limit, nacking without requeue",
			slog.String(logKeyTopic, topic),
			slog.Uint64("delivery_tag", delivery.DeliveryTag),
			slog.Int("len_capped", min(len(entry.ID), maxEntryIDLength*2)))
		s.nackPermanent(ch, delivery.DeliveryTag, topic)
		return
	}

	// Unified Entry validation at consumer entry: covers empty ID, missing
	// Topic+EventType, empty Payload, and metadata size limits in one place.
	// Defense-in-depth — production senders satisfy these invariants, but a
	// malformed broker delivery (e.g. tampered payload) must not reach handlers.
	if err := entry.Validate(); err != nil {
		slog.LogAttrs(ctx, slog.LevelError, "rabbitmq: invalid entry, nacking without requeue",
			slog.String(logKeyTopic, topic),
			slog.Uint64("delivery_tag", delivery.DeliveryTag),
			slog.String("error", err.Error()))
		s.nackPermanent(ch, delivery.DeliveryTag, topic)
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

	// Log handler-level error if present (separate from broker disposition).
	if res.Err != nil {
		slog.LogAttrs(deliveryCtx, slog.LevelWarn, "rabbitmq: handler reported error",
			slog.String(logKeyTopic, topic),
			slog.String(logKeyEventID, entry.ID),
			slog.String("disposition", res.Disposition.String()),
			slog.String("error", res.Err.Error()))
	}

	s.dispatchDisposition(deliveryCtx, ch, delivery.DeliveryTag, res, topic, entry.ID)
}

// dispatchDisposition executes the broker-level disposition and settles the
// idempotency receipt.
//
// DispositionAck: Commit FIRST (token-guarded), then broker Ack. If Commit
// fails (lease expired, Redis Lua token mismatch), Nack(requeue=true) so
// another holder retries. The previous Ack→Commit order could not roll back
// a broker delivery after Commit failure.
// ref: Temporal task-token validation (commit-time fencing)
// ref: MassTransit ValidateLockStatus (Ack 前最后一道门)
//
// DispositionReject/Requeue: broker Nack first, then Release the receipt so
// DLQ replay or redelivery can re-enter the Claim/Commit cycle cleanly.
func (s *Subscriber) dispatchDisposition(
	ctx context.Context,
	ch AMQPChannel,
	tag uint64,
	res outbox.HandleResult,
	topic, eventID string,
) {
	switch res.Disposition {
	case outbox.DispositionAck:
		s.dispatchAck(ctx, ch, tag, res, topic, eventID)
	case outbox.DispositionReject:
		if nackErr := ch.Nack(tag, false, false); nackErr != nil {
			slog.LogAttrs(ctx, slog.LevelError, "rabbitmq: nack(reject) failed",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyEventID, eventID),
				slog.String("error", nackErr.Error()))
		}
		releaseReceipt(ctx, res.Receipt, topic, eventID, "reject")
	case outbox.DispositionRequeue:
		if nackErr := ch.Nack(tag, false, true); nackErr != nil {
			slog.LogAttrs(ctx, slog.LevelError, "rabbitmq: nack(requeue) failed",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyEventID, eventID),
				slog.String("error", nackErr.Error()))
		}
		releaseReceipt(ctx, res.Receipt, topic, eventID, "requeue")
	default:
		slog.LogAttrs(ctx, slog.LevelError, "rabbitmq: unknown disposition, nacking with requeue",
			slog.String(logKeyTopic, topic),
			slog.String(logKeyEventID, eventID),
			slog.String("disposition", res.Disposition.String()))
		if nackErr := ch.Nack(tag, false, true); nackErr != nil {
			slog.LogAttrs(ctx, slog.LevelError, "rabbitmq: nack(requeue) failed for unknown disposition",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyEventID, eventID),
				slog.String("error", nackErr.Error()))
		}
		releaseReceipt(ctx, res.Receipt, topic, eventID, "unknown")
	}
}

// dispatchAck handles the Commit→Ack path for DispositionAck.
// If Commit fails, Nack(requeue=true) is issued instead of Ack.
func (s *Subscriber) dispatchAck(
	ctx context.Context,
	ch AMQPChannel,
	tag uint64,
	res outbox.HandleResult,
	topic, eventID string,
) {
	if res.Receipt != nil {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		commitErr := res.Receipt.Commit(rctx)
		cancel()
		if commitErr != nil {
			slog.LogAttrs(ctx, slog.LevelWarn, "rabbitmq: receipt commit failed (lease may have expired); requeuing instead of acking",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyEventID, eventID),
				slog.String("error", commitErr.Error()))
			if nackErr := ch.Nack(tag, false, true); nackErr != nil {
				slog.LogAttrs(ctx, slog.LevelError, "rabbitmq: nack(requeue) failed after commit failure",
					slog.String(logKeyTopic, topic),
					slog.String(logKeyEventID, eventID),
					slog.String("error", nackErr.Error()))
			}
			return
		}
	}
	if ackErr := ch.Ack(tag, false); ackErr != nil {
		slog.LogAttrs(ctx, slog.LevelError, "rabbitmq: ack failed",
			slog.String(logKeyTopic, topic),
			slog.String(logKeyEventID, eventID),
			slog.String("error", ackErr.Error()))
		// Receipt already committed; broker ack failure means the message will
		// be redelivered, but the idempotency key (ClaimDone) prevents double
		// processing on the next delivery.
	}
}

// releaseReceipt releases the idempotency receipt with a 5s detached timeout.
// Uses context.WithoutCancel so the operation completes even during graceful shutdown.
// reason is used for structured log fields.
func releaseReceipt(ctx context.Context, receipt outbox.Receipt, topic, eventID, reason string) {
	if receipt == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if relErr := receipt.Release(rctx); relErr != nil {
		slog.LogAttrs(rctx, slog.LevelError, "rabbitmq: receipt release failed",
			slog.String(logKeyTopic, topic),
			slog.String(logKeyEventID, eventID),
			slog.String("reason", reason),
			slog.String("error", relErr.Error()))
	}
}

// Close terminates all active subscriptions and waits for in-flight messages.
//
// Two-phase shutdown:
//  1. Signal all goroutines to stop via closeCh (and check pre-cancelled ctx).
//  2. Wait for all processDelivery goroutines (global s.wg) bounded by ctx.
//     If ctx expires, log a warning and return ErrAdapterAMQPCloseTimeout.
//  3. For any runs still in the runs map (subscribeOnce deferred removal is async),
//     call run.waitAndClose(ctx) which is idempotent (sync.Once guards ch.Close).
//
// The A19 fix guarantees that ch.Close is never called while processDelivery
// goroutines still hold a reference: subscribeOnce calls run.waitAndClose(localCtx)
// before returning, and Close's sweep is guarded by the same sync.Once.
//
// ref: uber-go/fx app.go StopTimeout — ctx carries the shared shutdown budget
// ref: amqp091-go channel.go — IsClosed short-circuit + ordered Cancel/drain/wg.Wait/Close
func (s *Subscriber) Close(ctx context.Context) error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(s.closeCh)

	if err := ctx.Err(); err != nil {
		return err
	}

	// Wait for all in-flight processDelivery goroutines (global wg).
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("rabbitmq: subscriber closed gracefully")
	case <-ctx.Done():
		s.runsMu.Lock()
		remaining := len(s.runs)
		s.runsMu.Unlock()
		slog.Warn("rabbitmq: subscriber shutdown budget exceeded",
			slog.Any("error", ctx.Err()),
			slog.Int("remaining_runs", remaining))
		return errcode.New(ErrAdapterAMQPCloseTimeout,
			fmt.Sprintf("rabbitmq: subscriber Close timed out with %d run(s) still active",
				remaining))
	}

	// Sweep any runs that subscribeOnce's defer has not yet removed. This is
	// a safety net — normally subscribeOnce's defer calls removeRun after
	// run.waitAndClose returns. run.waitAndClose is idempotent via sync.Once.
	s.runsMu.Lock()
	remaining := make([]*subscriptionRun, 0, len(s.runs))
	for r := range s.runs {
		remaining = append(remaining, r)
	}
	s.runsMu.Unlock()

	for _, r := range remaining {
		_ = r.waitAndClose(ctx)
	}

	return nil
}

// StopIntake implements outbox.SubscriberIntakeStopper. It stops the AMQP
// consumer from receiving new deliveries (broker-side basic.cancel) while
// allowing in-flight processDelivery goroutines and already-prefetched messages
// to complete naturally. Close() remains the hard-shutdown boundary
// (closeCh signal + wg.Wait bounded by ctx).
//
// Idempotent: safe to call multiple times. Safe to call concurrently with
// Subscribe and processDelivery in-flight.
//
// ref: ThreeDotsLabs/watermill-amqp subscriber.go — closedChan + basic.cancel
// consumerRef pairs an AMQP channel with its consumer tag so StopIntake can
// snapshot the active set and operate on it lock-free.
type consumerRef struct {
	ch  AMQPChannel
	tag string
}

// cancelConsumerWithBudget issues basic.cancel for a single consumer with an
// independent per-call timeout. Respects the outer ctx. Inner Cancel goroutine
// is bounded by the broker's own channel timeout; the buffered done channel
// prevents send-side goroutine leaks if the outer select races past.
func cancelConsumerWithBudget(ctx context.Context, c consumerRef, perCallTimeout time.Duration) {
	callCtx, cancelCall := context.WithTimeout(ctx, perCallTimeout)
	defer cancelCall()

	done := make(chan error, 1)
	go func() { done <- c.ch.Cancel(c.tag, false) }()

	select {
	case err := <-done:
		if err != nil {
			slog.Warn("rabbitmq: basic.cancel during StopIntake failed",
				slog.String("consumer_tag", c.tag),
				slog.Any("error", err))
		}
	case <-callCtx.Done():
		slog.Warn("rabbitmq: basic.cancel during StopIntake exceeded per-call budget or outer ctx cancelled",
			slog.String("consumer_tag", c.tag),
			slog.Any("error", callCtx.Err()))
	}
}

// StopIntake snapshots active runs, releases the lock, then issues basic.cancel
// concurrently via run.cancelWithBudget. Each individual Cancel is bounded by
// StopIntakePerCallTimeout so a single unresponsive broker cannot stall the
// whole shutdown path. The outer ctx gates the entire operation — if the
// caller's budget expires, StopIntake returns ctx.Err() without waiting for
// remaining goroutines.
//
// Invariants:
//   - runsMu is NEVER held across broker I/O (basic.cancel is a synchronous round-trip).
//   - Each Cancel call has an independent per-call deadline.
//   - On outer ctx cancel, returns promptly; leaked Cancel goroutines are bounded
//     by the per-call timeout and broker channel liveness.
//
// ref: Uber fx app.go StopTimeout (budget must be honoured);
// ref: Watermill router.go (stop intake → bounded drain pattern).
func (s *Subscriber) StopIntake(ctx context.Context) error {
	s.stopIntakeOnce.Do(func() { close(s.stopIntakeCh) })

	runs := s.snapshotActiveRuns()
	if len(runs) == 0 {
		return nil
	}

	perCallTimeout := s.config.StopIntakePerCallTimeout
	if perCallTimeout <= 0 {
		perCallTimeout = 2 * time.Second
	}

	var cancelWg sync.WaitGroup
	for _, r := range runs {
		if ctx.Err() != nil {
			break // outer budget expired; skip remaining dispatch
		}
		cancelWg.Add(1)
		go func(run *subscriptionRun) {
			defer cancelWg.Done()
			run.cancelWithBudget(ctx, perCallTimeout)
		}(r)
	}

	// Wait for every dispatched basic.cancel to complete (or for their own
	// per-call timeouts to fire) before reporting StopIntake done. This
	// ensures the broker has stopped pushing new deliveries when we return.
	//
	// Note: drain completion is NOT awaited here. consumeLoop's priority
	// select guarantees stopIntakeCh is observed before closeCh even if
	// both are closed by the time consumeLoop re-enters select, so
	// drainRemaining is always entered. Inside drainRemaining, buffered
	// deliveries are dispatched (priority) before abort signals.
	cancelDone := make(chan struct{})
	go func() { cancelWg.Wait(); close(cancelDone) }()
	select {
	case <-cancelDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// Wire format deserialization
// ---------------------------------------------------------------------------

// unmarshalDelivery deserializes a broker message body into an outbox.Entry.
//
// Requires a valid v1 WireMessage envelope (schemaVersion:"v1", non-empty id and
// eventType). Any other payload — legacy Entry JSON, raw bytes, unknown schema
// versions — is rejected with an error so processDelivery NACKs without requeue
// (permanent error, routes to DLX).
//
// Fail-closed semantics: legacy fallback has been removed. All relay producers
// MUST emit v1 envelopes via MarshalEnvelope.
//
// ref: Watermill message/router.go handleMessage (unknown type → Nack, no retry)
// ref: runtime/outbox/envelope.go UnmarshalEnvelope
func unmarshalDelivery(body []byte) (outbox.Entry, error) {
	return outboxrt.UnmarshalEnvelope("", body)
}
