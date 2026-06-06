package mqtt

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eclipse/paho.golang/paho"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time interface checks.
var (
	_ outbox.Subscriber              = (*Subscriber)(nil)
	_ outbox.SubscriberIntakeStopper = (*Subscriber)(nil)
)

const (
	// defaultSettlementTimeout bounds Settlement.Commit / Release calls. Mirrors
	// adapters/rabbitmq defaultRMQReceiptOpTimeout (5s): outlives caller
	// cancellation so the idempotency backend never stays inconsistent.
	defaultSettlementTimeout = 5 * time.Second
	// defaultStopIntakePerCallTimeout bounds any single unsubscribe (route cancel)
	// during StopIntake. Mirrors rabbitmq's per-call budget (2s).
	defaultStopIntakePerCallTimeout = 2 * time.Second
	// defaultStopIntakeDrainTimeout is the upper bound StopIntake waits for
	// in-flight handlers to settle. Mirrors rabbitmq's drain deadline (30s).
	defaultStopIntakeDrainTimeout = 30 * time.Second

	// stopIntakeInflightPollInterval is the cadence StopIntake polls the inflight
	// counter while draining. Polling (vs sync.WaitGroup.Wait) avoids the
	// Add-after-Wait panic (makeReceive runs in autopaho's read-loop callback);
	// 20ms keeps shutdown latency imperceptible. Mirrors adapters/rabbitmq
	// stopIntakeInflightPollInterval.
	stopIntakeInflightPollInterval = 20 * time.Millisecond

	// defaultMaxConcurrentHandlers bounds how many deliveries are processed
	// concurrently. paho calls OnPublishReceived synchronously on a single
	// inbound-dispatch goroutine, so makeReceive hands each delivery to a bounded
	// worker pool — otherwise one slow/blocked handler stalls intake for the whole
	// connection. The bound doubles as back-pressure: when saturated, the callback
	// blocks (the broker's Receive Maximum bounds in-flight unacked QoS1 anyway).
	defaultMaxConcurrentHandlers = 16

	// defaultSubscriberQoS is the MQTT QoS used for SUBSCRIBE when SubscriberConfig
	// leaves QoS unset. QoS 1 (at-least-once) pairs with the publisher's QoS 1 and
	// the manual-ack idempotency model.
	defaultSubscriberQoS byte = 1

	// Structured log field keys (snake_case per observability.md).
	logKeyClientID = "client_id"
	logKeyTopic    = "topic"
	logKeyEventID  = "event_id"
	// logKeyDLXTopic is the $dead/<topic> sink target logged on dead-letter routing.
	logKeyDLXTopic = "dlx_topic"
)

// SubscriberConfig configures how a Subscriber consumes messages.
//
// There is no DLXExchange: MQTT has no broker-native dead-letter exchange. Poison
// (unmarshal-failure) and permanent-reject messages are routed to an app-level
// "$dead/<topic>" sink (see deadletter.go routeDeadLetter) before being
// acked-as-consumed, so they cannot block intake forever yet remain auditable.
type SubscriberConfig struct {
	// QoS is the MQTT QoS requested in the SUBSCRIBE packet. 0 → defaults to 1.
	// Only QoS 0 and 1 are supported; QoS 2 is NOT supported by the manual-ack
	// idempotency model (the adapter relies on at-least-once + idempotency keys,
	// not the QoS-2 exactly-once handshake). QoS 0 is forced to 1 by setDefaults.
	QoS byte

	// SettlementTimeout bounds each Settlement.Commit / Release call. 0 → 5s.
	SettlementTimeout time.Duration

	// StopIntakePerCallTimeout bounds any single route-cancel (UNSUBSCRIBE) issued
	// during StopIntake. 0 → 2s.
	StopIntakePerCallTimeout time.Duration

	// StopIntakeDrainTimeout is the total upper bound for StopIntake to wait for
	// in-flight handler goroutines to settle. 0 → 30s.
	StopIntakeDrainTimeout time.Duration

	// MaxConcurrentHandlers bounds how many received deliveries are processed
	// concurrently (handler goroutines). 0 → 16. Each delivery is handed to a
	// bounded worker pool so a slow handler does not stall the autopaho inbound
	// dispatch loop. Keep ≤ the broker/client MQTT v5 Receive Maximum (the
	// in-flight unacked QoS1 bound) so the pool, not the broker window, is the
	// effective concurrency ceiling.
	MaxConcurrentHandlers int
}

// setDefaults populates zero-valued fields with safe defaults.
func (sc *SubscriberConfig) setDefaults() {
	if sc.QoS == 0 {
		sc.QoS = defaultSubscriberQoS
	}
	if sc.SettlementTimeout <= 0 {
		sc.SettlementTimeout = defaultSettlementTimeout
	}
	if sc.StopIntakePerCallTimeout <= 0 {
		sc.StopIntakePerCallTimeout = defaultStopIntakePerCallTimeout
	}
	if sc.StopIntakeDrainTimeout <= 0 {
		sc.StopIntakeDrainTimeout = defaultStopIntakeDrainTimeout
	}
	if sc.MaxConcurrentHandlers <= 0 {
		sc.MaxConcurrentHandlers = defaultMaxConcurrentHandlers
	}
}

// Subscriber implements outbox.Subscriber + outbox.SubscriberIntakeStopper for
// MQTT v5 brokers using shared subscriptions ("$share/{group}/{filter}") for
// consumer-group semantics.
//
// Concurrency: autopaho owns reconnection; onConnectionUp re-arms registered
// routes, so Subscribe does not run a manual reconnect loop — it blocks until
// ctx is canceled (or the subscriber is closed) after the initial SUBSCRIBE
// confirms. Each received PUBLISH is dispatched by the connection's read loop
// into makeReceive, which is tracked by wg for graceful drain.
//
// Consumer: cg-{ConsumerGroup}-{topic}
// Idempotency: Claimer (two-phase Claim/Commit/Release) via ConsumerBase, TTL 24h
// Disposition: Ack on success / Requeue → left unacked, broker redelivers on
// reconnect (Option C, ADR-050 §6) on transient / Reject → routed to
// $dead/<topic> then acked-as-poison on permanent.
// DLX: app-level $dead/<topic> (no broker-native DLX in MQTT); see deadletter.go.
//
// ref: adapters/rabbitmq/subscriber.go dispatchDisposition / StopIntake.
type Subscriber struct {
	clk       clock.Clock
	conn      *Connection
	ns        TopicNamespace
	config    SubscriberConfig
	collector SubscriberCollector

	closed atomic.Bool

	// inflight is the StopIntake drain primitive: the count of in-flight delivery
	// callbacks. makeReceive increments it BEFORE the stopIntakeCh check and
	// decrements via defer; StopIntake polls it to zero. A sync.WaitGroup is
	// intentionally NOT used — makeReceive runs in autopaho's read-loop callback,
	// so wg.Add(1) would race StopIntake's wg.Wait ("Add called concurrently with
	// Wait" panic). Mirrors adapters/rabbitmq's inflight-counter poll-drain
	// (waitInflightDrain).
	inflight atomic.Int64

	// workerSem is a bounded semaphore (cap = config.MaxConcurrentHandlers).
	// makeReceive acquires a slot before spawning a per-delivery handler
	// goroutine, so a slow/blocked handler bounds — never stalls — the autopaho
	// inbound dispatch loop (paho calls OnPublishReceived synchronously on one
	// goroutine). When saturated, acquire blocks (intended back-pressure).
	workerSem chan struct{}

	// closeCh is closed by Close to signal all blocked Subscribe calls to return.
	closeCh   chan struct{}
	closeOnce sync.Once

	// stopIntakeCh is closed by StopIntake to signal makeReceive to stop accepting
	// new deliveries (ack-noop drop) while in-flight handlers drain.
	stopIntakeCh   chan struct{}
	stopIntakeOnce sync.Once

	// readyMu guards readyChans, the per-(consumerGroup, topic) ready-signal
	// registry. Ready returns the channel for a subscription (creating it lazily);
	// Subscribe closes it once that subscription's SUBSCRIBE confirms (SUBACK).
	// The key is (consumerGroup, topic), NOT topic alone: two subscriptions on the
	// same topic with different consumer groups are distinct $share subscriptions
	// and must have independent ready signals (else group B's Ready would close
	// when group A's SUBACK arrives). Correct even if Ready is called before
	// Subscribe — both lazily create the same channel.
	readyMu    sync.Mutex
	readyChans map[string]chan struct{}

	// cancelMu guards cancels, the set of active route-cancel funcs returned by
	// conn.Subscribe. StopIntake / Close iterate it to unsubscribe active routes.
	cancelMu sync.Mutex
	cancels  []func()
}

// SubscriberOption configures optional behavior of a Subscriber.
type SubscriberOption func(*Subscriber)

// WithSubscriberCollector injects a SubscriberCollector that records consume-side
// metrics. Default is NoopSubscriberCollector.
func WithSubscriberCollector(c SubscriberCollector) SubscriberOption {
	return func(s *Subscriber) {
		if c != nil {
			s.collector = c
		}
	}
}

// NewSubscriber constructs a Subscriber bound to conn and ns. clk is a required
// positional parameter (CLOCK-POSITIONAL-INJECTION-01).
//
// Returns an error if conn is nil or ns is the zero-value namespace (which would
// fail every MintFilter). A nil clock is a programmer error (panic via
// clock.MustHaveClock), mirroring NewPublisher's signature split.
func NewSubscriber(
	clk clock.Clock, conn *Connection, ns TopicNamespace, config SubscriberConfig, opts ...SubscriberOption,
) (*Subscriber, error) {
	clock.MustHaveClock(clk, "mqtt.NewSubscriber")
	if conn == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterMQTTInvalidConfig,
			"mqtt: NewSubscriber requires non-nil Connection")
	}
	if ns.String() == "" {
		return nil, errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidTopicNamespace,
			"mqtt: NewSubscriber requires non-zero TopicNamespace")
	}
	// QoS 2 is not supported by the manual-ack idempotency model (which relies on
	// at-least-once + idempotency keys, not the QoS-2 exactly-once handshake).
	// Reject it fail-closed at construction rather than silently subscribing at
	// QoS 2. QoS 0 is permitted (setDefaults promotes it to 1).
	if config.QoS > 1 {
		return nil, errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig,
			"mqtt: SubscriberConfig.QoS must be 0 or 1; QoS 2 is unsupported by the manual-ack idempotency model")
	}
	config.setDefaults()
	s := &Subscriber{
		clk:          clk,
		conn:         conn,
		ns:           ns,
		config:       config,
		collector:    NoopSubscriberCollector{},
		closeCh:      make(chan struct{}),
		stopIntakeCh: make(chan struct{}),
		readyChans:   make(map[string]chan struct{}),
		workerSem:    make(chan struct{}, config.MaxConcurrentHandlers),
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Setup validates the subscription filter against the namespace. MQTT has no
// broker-side topology to pre-declare (no exchanges / queues), so Setup is a
// pure validation gate: it returns the SubscribeOK error if sub.Topic is not a
// valid filter under the namespace, else nil.
//
// Setup validates ONLY the namespace boundary (and wildcard placement) of the
// filter; it does NOT validate the consumer group. The consumer-group
// requirement (non-empty + charset) is enforced later at Subscribe time via
// MintFilter, which composes the "$share/{group}/{filter}" wire form.
func (s *Subscriber) Setup(_ context.Context, sub outbox.Subscription) error {
	return s.ns.SubscribeOK(sub.Topic)
}

// Ready returns a channel that is closed once the SUBSCRIBE for sub.Topic has
// been confirmed (SUBACK). It is correct to call Ready before Subscribe: both
// lazily resolve the same per-topic channel, and Subscribe closes it after
// conn.Subscribe returns.
func (s *Subscriber) Ready(sub outbox.Subscription) <-chan struct{} {
	return s.readyChan(sub.ConsumerGroup, sub.Topic)
}

// readyKey composes the per-subscription ready-registry key. The separator is
// NUL ("\x00"), which MQTT v5 forbids in topic names/filters (§1.5.4) and which
// the consumer-group charset (^[a-z0-9_-]+$) excludes, so distinct
// (group, topic) pairs never collide.
func readyKey(consumerGroup, topic string) string {
	return consumerGroup + "\x00" + topic
}

// readyChan returns the ready channel for (consumerGroup, topic), creating it on
// first access.
func (s *Subscriber) readyChan(consumerGroup, topic string) chan struct{} {
	key := readyKey(consumerGroup, topic)
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	ch, ok := s.readyChans[key]
	if !ok {
		ch = make(chan struct{})
		s.readyChans[key] = ch
	}
	return ch
}

// signalReady closes the ready channel for (consumerGroup, topic) exactly once.
func (s *Subscriber) signalReady(consumerGroup, topic string) {
	ch := s.readyChan(consumerGroup, topic)
	select {
	case <-ch:
		// already closed
	default:
		close(ch)
	}
}

// Subscribe registers a handler for sub and blocks until ctx is canceled or the
// subscriber is closed. It mints the shared-subscription filter, sends the
// SUBSCRIBE via conn.Subscribe (which returns after SUBACK), signals Ready, then
// blocks. autopaho self-heals the connection and onConnectionUp re-arms routes,
// so no manual reconnect loop is needed here.
func (s *Subscriber) Subscribe(ctx context.Context, sub outbox.Subscription, handler outbox.SubscriberHandler) error {
	if s.closed.Load() {
		return errcode.New(errcode.KindInternal, ErrAdapterMQTTClosed, "mqtt: subscriber is closed")
	}

	f, err := s.ns.MintFilter(sub.ConsumerGroup, sub.Topic)
	if err != nil {
		return err
	}

	// Derive a subCtx canceled when either the parent ctx is done or the
	// subscriber is closed, so the dispatch closure and this Subscribe call
	// observe shutdown promptly even if the parent ctx has no deadline.
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	go func() {
		select {
		case <-s.closeCh:
			subCancel()
		case <-subCtx.Done():
		}
	}()
	cancel, err := s.conn.Subscribe(subCtx, f, s.config.QoS, s.makeReceive(subCtx, handler))
	if err != nil {
		return err
	}
	defer cancel()
	s.trackCancel(cancel)
	s.signalReady(sub.ConsumerGroup, sub.Topic)

	// Block until ctx canceled / subscriber closed. Clean exit returns nil.
	<-subCtx.Done()
	return nil
}

// trackCancel records a route-cancel func so StopIntake / Close can unsubscribe.
func (s *Subscriber) trackCancel(cancel func()) {
	s.cancelMu.Lock()
	s.cancels = append(s.cancels, cancel)
	s.cancelMu.Unlock()
}

// makeReceive returns a receiveHandler that tracks each delivery against wg for
// graceful drain and dispatches it to processDelivery. If StopIntake has already
// fired, the delivery is dropped (ack-noop) so it is redelivered after restart
// rather than processed during shutdown.
func (s *Subscriber) makeReceive(subCtx context.Context, handler outbox.SubscriberHandler) receiveHandler {
	return func(ctx context.Context, pb *paho.Publish) {
		// Count this delivery in-flight at entry (before any drop / queue decision)
		// so the StopIntake poll-drain can never read a zero count while a delivery
		// is mid-flight. The decrement is owned by exactly one path: an inline drop
		// here, or the worker goroutine's defer.
		s.inflight.Add(1)
		select {
		case <-s.stopIntakeCh:
			// Intake stopped: do not process or ack. Leaving the message unacked
			// lets the broker redeliver after session resume / reconnect.
			s.inflight.Add(-1)
			s.logIntakeStoppedDrop(pb)
			return
		default:
		}
		// Hand the delivery to a bounded worker pool so a slow/blocked handler does
		// not stall the autopaho inbound dispatch loop (paho calls OnPublishReceived
		// synchronously on a single goroutine). Acquiring a slot blocks when the
		// pool is saturated — intended back-pressure — but must not outlive
		// shutdown, so the acquire races stopIntakeCh.
		select {
		case s.workerSem <- struct{}{}:
		case <-s.stopIntakeCh:
			s.inflight.Add(-1)
			s.logIntakeStoppedDrop(pb)
			return
		}
		// Prefer the connection-supplied ctx (subscription-scoped); fall back to
		// subCtx if the read loop hands a nil ctx.
		deliveryCtx := ctx
		if deliveryCtx == nil {
			deliveryCtx = subCtx
		}
		go func() {
			defer func() {
				<-s.workerSem
				s.inflight.Add(-1)
			}()
			s.processDelivery(deliveryCtx, pb, handler)
		}()
	}
}

// logIntakeStoppedDrop records that a delivery was dropped (left unacked for
// broker redelivery) because StopIntake has fired.
func (s *Subscriber) logIntakeStoppedDrop(pb *paho.Publish) {
	slog.Info("mqtt: intake stopped, dropping delivery for redelivery",
		slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
		slog.String(logKeyTopic, safeTopicForLog(pb.Topic)))
}

// processDelivery decodes the wire envelope and dispatches to the handler.
//
// On unmarshal failure (POISON): the bytes are permanently undecodable, so the
// raw payload is routed to $dead/<pb.Topic> (the broker-delivered topic is known
// even though the envelope is not) and the message is acked-as-consumed (it
// cannot block intake forever). The handler is NOT invoked.
func (s *Subscriber) processDelivery(ctx context.Context, pb *paho.Publish, handler outbox.SubscriberHandler) {
	entry, err := outbox.UnmarshalEnvelope(pb.Topic, pb.Payload)
	if err != nil {
		// Classify the poison with a stable code (ERR_ADAPTER_MQTT_UNMARSHAL_ENVELOPE)
		// so operators can grep it; the handler is never invoked for poison.
		poisonErr := errcode.Wrap(errcode.KindInvalid, ErrAdapterMQTTUnmarshalEnvelope,
			"mqtt: unmarshal envelope failed", err)
		slog.LogAttrs(ctx, slog.LevelError, "mqtt: unmarshal envelope failed, routing raw payload to $dead then acking poison",
			slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
			slog.String(logKeyTopic, safeTopicForLog(pb.Topic)),
			slog.Any("error", poisonErr))
		s.collector.RecordConsumeFailure(ctx, consumeReasonUnmarshal)
		// Capture the undecodable bytes in $dead/<pb.Topic> before the ack. The
		// envelope is unparseable, so use the broker-delivered topic and the raw
		// payload. routeDeadLetter is fail-closed (logs + skips on any error).
		s.routeDeadLetter(ctx, pb.Topic, pb.Payload, consumeReasonUnmarshal)
		// ackPoison logs its own ack failure. The unmarshal path has no Settlement
		// or SettlementObservers to notify (the handler was never invoked); if the
		// poison ack itself fails, record a distinct ack-failed metric (the broker
		// will redeliver the un-decodable message and it will be re-acked).
		if s.ackPoison(ctx, pb, "unmarshal") != nil {
			s.collector.RecordConsumeFailure(ctx, consumeReasonAckFailed)
		}
		return
	}

	start := s.clk.Now()
	res, settlement := handler(ctx, entry)
	s.dispatchDisposition(ctx, pb, res, settlement, entry, start)
}

// dispatchDisposition routes a handler result to the broker (ack / leave-unacked)
// and settles the idempotency receipt. Mirrors adapters/rabbitmq dispatchDisposition.
func (s *Subscriber) dispatchDisposition(
	ctx context.Context, pb *paho.Publish, res outbox.DeliveryOutcome,
	settlement outbox.Settlement, entry outbox.Entry, start time.Time,
) {
	switch res.Disposition {
	case outbox.DispositionAck:
		s.dispatchAck(ctx, pb, res, settlement, entry, start)
	case outbox.DispositionReject:
		// Permanent failure: route the envelope to $dead/<topic> for ops audit,
		// then ack-as-poison to stop redelivery. routeDeadLetter is fail-closed
		// (logs + skips on any error) so the ack always proceeds.
		//
		// Order is a correctness invariant: route → ackPoison (broker disposition)
		// → releaseSettlement. The claim is released only AFTER the PUBACK finalizes
		// the broker disposition, so a connection drop mid-settle cannot redeliver
		// the message to another instance that then re-claims a freed claim (mirrors
		// rabbitmq's Nack-before-release). routeDeadLetter's broker round-trip would
		// otherwise widen that window.
		slog.LogAttrs(ctx, slog.LevelError, "mqtt: handler rejected entry, routing to $dead then acking poison",
			slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
			slog.String(logKeyTopic, entry.Topic()),
			slog.String(logKeyEventID, entry.ID()),
			slog.Any("error", res.Err))
		s.collector.RecordConsumeFailure(ctx, consumeReasonReject)
		s.routeDeadLetter(ctx, entry.Topic(), pb.Payload, consumeReasonReject)
		ackErr := s.ackPoison(ctx, pb, "reject")
		s.releaseSettlement(ctx, settlement, entry, "reject")
		if ackErr != nil {
			outbox.NotifySettlement(ctx, res, entry, outbox.DispositionReject, outbox.SettlementResultAckFailed, ackErr)
		} else {
			outbox.NotifySettlement(ctx, res, entry, outbox.DispositionReject, outbox.RejectSettlementResult(res), nil)
		}
	case outbox.DispositionRequeue:
		// Option C (ADR-050 §6): MQTT is a transport, not a work queue. Leave the
		// message unacked so the broker redelivers on session resume / reconnect;
		// Release the claim so redelivery re-enters the Claim cycle cleanly.
		//
		// ConsumerBase is the sole retry layer — it retries transient handler
		// failures in-process and converts exhaustion to Reject BEFORE the adapter,
		// so a Requeue reaching here is a degraded-state signal (graceful shutdown /
		// idempotency backend down / claim contention), where reconnect-redelivery
		// is the correct behavior. There is deliberately no app-level re-dispatch
		// loop and no $dead routing on Requeue (those would be Option A).
		slog.LogAttrs(ctx, slog.LevelWarn, "mqtt: handler requeued entry, leaving unacked for reconnect redelivery (degraded-state signal)",
			slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
			slog.String(logKeyTopic, entry.Topic()),
			slog.String(logKeyEventID, entry.ID()),
			slog.Any("error", res.Err))
		s.releaseSettlement(ctx, settlement, entry, "requeue")
		s.collector.RecordConsumeFailure(ctx, consumeReasonRequeue)
		outbox.NotifySettlement(ctx, res, entry, outbox.DispositionRequeue, outbox.SettlementResultSuccess, nil)
	default:
		// Zero / invalid Disposition: treat as Requeue (leave unacked) + Release.
		// Same Option C reconnect-redelivery semantics as Requeue; no $dead routing.
		slog.LogAttrs(ctx, slog.LevelError, "mqtt: unknown disposition, leaving unacked (treated as requeue)",
			slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
			slog.String(logKeyTopic, entry.Topic()),
			slog.String(logKeyEventID, entry.ID()),
			slog.String("disposition", res.Disposition.String()))
		s.releaseSettlement(ctx, settlement, entry, "unknown")
		s.collector.RecordConsumeFailure(ctx, consumeReasonUnknownDisposition)
		outbox.NotifySettlement(ctx, res, entry, outbox.DispositionRequeue, outbox.SettlementResultSuccess, nil)
	}
}

// dispatchAck handles the Commit→Ack path for DispositionAck. Settlement.Commit
// is token-guarded and called BEFORE the broker ack: if Commit fails (lease
// expired), the message is left unacked (broker redelivers) and the claim is
// released so another holder retries — mirroring rabbitmq dispatchAck.
func (s *Subscriber) dispatchAck(
	ctx context.Context, pb *paho.Publish, res outbox.DeliveryOutcome,
	settlement outbox.Settlement, entry outbox.Entry, start time.Time,
) {
	if settlement != nil {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.config.SettlementTimeout)
		commitErr := settlement.Commit(rctx)
		cancel()
		if commitErr != nil {
			slog.LogAttrs(ctx, slog.LevelError, "mqtt: settlement commit failed (lease may have expired); leaving unacked",
				slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
				slog.String(logKeyTopic, entry.Topic()),
				slog.String(logKeyEventID, entry.ID()),
				slog.Any("error", commitErr))
			s.releaseSettlement(ctx, settlement, entry, "commit_failed")
			s.collector.RecordConsumeFailure(ctx, consumeReasonCommitFailed)
			// Commit failure → message left unacked (broker redelivers). Mirrors
			// rabbitmq: report as Requeue/commit_failed to settlement observers.
			outbox.NotifySettlement(ctx, res, entry, outbox.DispositionRequeue, outbox.SettlementResultCommitFailed, commitErr)
			return
		}
	}
	if ackErr := s.conn.ack(pb); ackErr != nil {
		// Settlement already committed; broker ack failure means the message is
		// redelivered, but the idempotency key (ClaimDone) prevents reprocessing.
		slog.LogAttrs(ctx, slog.LevelError, "mqtt: ack failed after commit; will redeliver (idempotency guards reprocess)",
			slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
			slog.String(logKeyTopic, entry.Topic()),
			slog.String(logKeyEventID, entry.ID()),
			slog.Any("error", ackErr))
		s.collector.RecordConsumeFailure(ctx, consumeReasonAckFailed)
		outbox.NotifySettlement(ctx, res, entry, outbox.DispositionAck, outbox.SettlementResultAckFailed, ackErr)
		return
	}
	s.collector.RecordConsumeSuccess(ctx, s.clk.Since(start))
	outbox.NotifySettlement(ctx, res, entry, outbox.DispositionAck, outbox.SettlementResultSuccess, nil)
}

// ackPoison acks a message that must be consumed-as-poison (unmarshal failure or
// DispositionReject) so it cannot block intake forever. PR-4 will publish to
// $dead/<topic> before this ack. The ack error is logged here and also returned
// so callers on the Reject path can classify the settlement outcome
// (SettlementResultAckFailed vs Success) for SettlementObservers.
func (s *Subscriber) ackPoison(ctx context.Context, pb *paho.Publish, reason string) error {
	if ackErr := s.conn.ack(pb); ackErr != nil {
		slog.LogAttrs(ctx, slog.LevelError, "mqtt: ack of poison message failed",
			slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
			slog.String(logKeyTopic, safeTopicForLog(pb.Topic)),
			slog.String("reason", reason),
			slog.Any("error", ackErr))
		return ackErr
	}
	return nil
}

// releaseSettlement releases the idempotency settlement bounded by
// SettlementTimeout. Uses context.WithoutCancel so the operation completes even
// during graceful shutdown. nil settlement (ClaimDone / ClaimBusy / fail-open)
// is a no-op.
func (s *Subscriber) releaseSettlement(ctx context.Context, settlement outbox.Settlement, entry outbox.Entry, reason string) {
	if settlement == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.config.SettlementTimeout)
	defer cancel()
	if relErr := settlement.Release(rctx); relErr != nil {
		slog.LogAttrs(rctx, slog.LevelError, "mqtt: settlement release failed",
			slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
			slog.String(logKeyTopic, entry.Topic()),
			slog.String(logKeyEventID, entry.ID()),
			slog.String("reason", reason),
			slog.Any("error", relErr))
	}
}

// StopIntake stops new deliveries from being processed (makeReceive drops them
// for redelivery), cancels active subscriptions (UNSUBSCRIBE), then polls the
// inflight counter to zero bounded by StopIntakeDrainTimeout. Idempotent.
//
// Drain polls the atomic inflight counter rather than sync.WaitGroup.Wait:
// makeReceive's inflight.Add(1) runs in autopaho's read-loop callback and would
// race a WaitGroup.Wait ("Add called concurrently with Wait" panic). An atomic
// counter has no such contract (mirrors adapters/rabbitmq waitInflightDrain).
//
// On timeout it returns a wrapped error and logs a Warn with the residual count;
// unfinished handler goroutines are NOT killed (Go has no goroutine cancel).
func (s *Subscriber) StopIntake(ctx context.Context) error {
	s.stopIntakeOnce.Do(func() { close(s.stopIntakeCh) })
	s.cancelActiveRoutes(ctx)

	if s.inflight.Load() == 0 {
		return nil
	}
	drainTimer := s.clk.NewTimerAt(s.clk.Now().Add(s.config.StopIntakeDrainTimeout))
	defer drainTimer.Stop()
	for {
		pollTimer := s.clk.NewTimerAt(s.clk.Now().Add(stopIntakeInflightPollInterval))
		select {
		case <-pollTimer.C():
			if s.inflight.Load() == 0 {
				return nil
			}
		case <-drainTimer.C():
			pollTimer.Stop()
			slog.Warn("mqtt: StopIntake drain timeout, returning fail-closed",
				slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
				slog.Duration("budget", s.config.StopIntakeDrainTimeout),
				slog.Int64("residual", s.inflight.Load()))
			return errcode.New(errcode.KindInternal, ErrAdapterMQTTSubscriberCloseTimeout,
				"mqtt: StopIntake drain budget exceeded")
		case <-ctx.Done():
			pollTimer.Stop()
			return ctx.Err()
		}
	}
}

// cancelActiveRoutes invokes every tracked route-cancel func (UNSUBSCRIBE),
// each bounded by StopIntakePerCallTimeout via a goroutine + select so a hung
// broker cannot stall shutdown. The cancel list is snapshotted under cancelMu.
func (s *Subscriber) cancelActiveRoutes(ctx context.Context) {
	s.cancelMu.Lock()
	cancels := make([]func(), len(s.cancels))
	copy(cancels, s.cancels)
	s.cancels = nil
	s.cancelMu.Unlock()

	for _, cancel := range cancels {
		s.cancelWithBudget(ctx, cancel)
	}
}

// cancelWithBudget runs a single route-cancel func bounded by
// StopIntakePerCallTimeout. The cancel func itself (conn UNSUBSCRIBE) logs its
// own errors; this only bounds how long we wait for it.
func (s *Subscriber) cancelWithBudget(ctx context.Context, cancel func()) {
	callCtx, cancelCall := context.WithTimeout(ctx, s.config.StopIntakePerCallTimeout)
	defer cancelCall()
	done := make(chan struct{})
	go func() { cancel(); close(done) }()
	select {
	case <-done:
	case <-callCtx.Done():
		slog.Warn("mqtt: route cancel during StopIntake exceeded per-call budget or ctx canceled",
			slog.String(logKeyClientID, s.conn.cfg.ClientID.String()),
			slog.Any("error", callCtx.Err()))
	}
}

// Close shuts the subscriber down idempotently. It signals all blocked Subscribe
// calls, runs StopIntake (cancel routes + drain in-flight handlers), and does
// NOT close the underlying Connection (lifecycle owned by bootstrap).
func (s *Subscriber) Close(ctx context.Context) error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.closeOnce.Do(func() { close(s.closeCh) })
	return s.StopIntake(ctx)
}
