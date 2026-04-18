// Package eventbus provides an in-memory implementation of kernel/outbox.Publisher
// and kernel/outbox.Subscriber for development and testing.
//
// ref: ThreeDotsLabs/watermill message/message.go — Message model, Ack/Nack pattern
// Adopted: topic-based pub/sub, callback handler pattern.
// Deviated: in-memory channel-based delivery (at-most-once, lost on restart);
// built-in retry with exponential backoff (3 attempts) + dead letter slice.
package eventbus

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	outboxrt "github.com/ghbvf/gocell/runtime/outbox"
)

const (
	maxRetries     = 3
	baseRetryDelay = 100 * time.Millisecond
	maxRetryDelay  = 30 * time.Second

	// TopicConfigChanged is the canonical event topic for config change
	// events. Cells that publish or subscribe to config changes should
	// reference this constant instead of defining their own.
	TopicConfigChanged = "event.config.changed.v1"
)

// DeadLetter represents a message that exhausted retries.
type DeadLetter struct {
	Topic   string
	Entry   outbox.Entry
	LastErr error
}

// subscription tracks a single subscriber goroutine.
type subscription struct {
	ch     chan outbox.Entry
	cancel context.CancelFunc
	done   chan struct{}
}

// InMemoryEventBus is a channel-based event bus for development and testing.
// It implements both outbox.Publisher and outbox.Subscriber.
// Semantics: at-most-once delivery (messages are lost on process restart).
//
// ConsumerGroup dispatch:
//   - same consumerGroup on same topic: round-robin (competing consumers)
//   - different consumerGroups on same topic: each group gets a copy (fanout)
//   - empty consumerGroup: broadcast to every subscriber (backward compatible)
type InMemoryEventBus struct {
	mu sync.RWMutex
	// groupSubs: topic → consumerGroup → *groupState
	// Each groupState holds the subscriber list and an atomic round-robin
	// counter for competing dispatch. Empty consumerGroup ("" key) entries
	// are broadcast individually.
	groupSubs     map[string]map[string]*groupState
	bufSize       int
	closed        bool
	deadLettersMu sync.Mutex
	deadLetters   []DeadLetter

	// readyMu guards readyChans. Separate from mu to avoid lock ordering issues.
	readyMu    sync.Mutex
	readyChans map[string]chan struct{} // key: consumerGroup + "|" + topic
}

// groupState tracks subscribers and round-robin index for a consumer group.
type groupState struct {
	subs  []*subscription
	rrIdx atomic.Uint64 // round-robin index for competing dispatch (atomic: accessed under RLock)
}

// Option configures the InMemoryEventBus.
type Option func(*InMemoryEventBus)

// WithBufferSize sets the channel buffer size per subscriber. Default is 256.
func WithBufferSize(size int) Option {
	return func(b *InMemoryEventBus) {
		if size > 0 {
			b.bufSize = size
		}
	}
}

// New creates an InMemoryEventBus.
func New(opts ...Option) *InMemoryEventBus {
	b := &InMemoryEventBus{
		groupSubs:  make(map[string]map[string]*groupState),
		readyChans: make(map[string]chan struct{}),
		bufSize:    256,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Publish sends payload to subscribers of the given topic.
//
// ConsumerGroup dispatch:
//   - For each named consumer group: pick ONE subscriber via round-robin
//   - For the empty-group ("") bucket: send to ALL subscribers (broadcast)
//
// Non-blocking: if a subscriber's buffer is full, the message is dropped
// (logged as warning).
//
// Envelope handling: when Publish is invoked by an outbox relay, payload is
// a JSON-encoded wire envelope (outbox.WireEnvelope) wrapping the business
// payload. The bus unwraps it so subscribers always see the business payload
// in Entry.Payload, matching the semantics of the RabbitMQ subscriber path.
// Non-envelope payloads (direct publish from cells) are forwarded unchanged.
//
// Regression guard: before this unwrap, the PG mode (relay → in-memory bus)
// silently delivered the envelope as-is; subscribers parsed the envelope
// fields as business fields (empty Action, etc.) and ACKed unknown-action
// events, causing complete event loss. Kept symmetric with
// adapters/rabbitmq.unmarshalDelivery.
func (b *InMemoryEventBus) Publish(_ context.Context, topic string, payload []byte) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return errcode.New(errcode.ErrBusClosed, "eventbus: bus is closed")
	}

	entry := unmarshalInboundEntry(topic, payload)
	for group, gs := range b.groupSubs[topic] {
		if len(gs.subs) == 0 {
			continue
		}
		b.dispatchToGroup(topic, group, gs, entry)
	}
	return nil
}

// dispatchToGroup delivers entry to the appropriate subscriber(s) in gs.
// Empty group: broadcast to all. Named group: round-robin to one.
func (b *InMemoryEventBus) dispatchToGroup(topic, group string, gs *groupState, entry outbox.Entry) {
	if group == "" {
		b.broadcast(topic, gs, entry)
	} else {
		b.roundRobin(topic, group, gs, entry)
	}
}

// broadcast delivers entry to every subscriber in gs (empty-group fanout).
func (b *InMemoryEventBus) broadcast(topic string, gs *groupState, entry outbox.Entry) {
	for _, sub := range gs.subs {
		select {
		case sub.ch <- entry:
		default:
			slog.Warn("eventbus: subscriber buffer full, message dropped",
				slog.String("topic", topic),
			)
		}
	}
}

// roundRobin delivers entry to one subscriber in gs via atomic round-robin.
func (b *InMemoryEventBus) roundRobin(topic, group string, gs *groupState, entry outbox.Entry) {
	rrVal := gs.rrIdx.Add(1) - 1 // atomic increment, use previous value
	idx := rrVal % uint64(len(gs.subs))
	sub := gs.subs[idx]
	select {
	case sub.ch <- entry:
	default:
		slog.Warn("eventbus: subscriber buffer full, message dropped",
			slog.String("topic", topic),
			slog.String("consumer_group", group),
		)
	}
}

// Setup implements outbox.Subscriber. InMemoryEventBus requires no topology
// pre-declaration; returns nil immediately.
func (b *InMemoryEventBus) Setup(_ context.Context, _ outbox.Subscription) error {
	return nil
}

// Ready implements outbox.Subscriber. Returns a channel that closes once
// Subscribe has been called for the given subscription (i.e., the subscription
// is registered and ready to receive messages). This prevents the
// publish-before-subscribe race in tests that use waitForSubscription.
//
// The key is sub.ConsumerGroup + "|" + sub.Topic so that different consumer
// groups on the same topic each get an independent ready signal.
func (b *InMemoryEventBus) Ready(sub outbox.Subscription) <-chan struct{} {
	key := sub.ConsumerGroup + "|" + sub.Topic
	b.readyMu.Lock()
	defer b.readyMu.Unlock()
	if ch, ok := b.readyChans[key]; ok {
		return ch
	}
	// No Subscribe call yet — create an open channel that Subscribe will close.
	ch := make(chan struct{})
	b.readyChans[key] = ch
	return ch
}

// Subscribe registers an EntryHandler for the given subscription. It blocks
// until ctx is cancelled or the bus is closed.
//
// Consumer: cg-eventbus-{sub.ConsumerGroup}-{sub.Topic}
// Idempotency key: N/A (in-memory, no persistence)
// ACK timing: after handler returns DispositionAck
// Retry: transient errors -> retry 3x with exponential backoff / permanent -> dead letter
//
// sub.ConsumerGroup selects the dispatch mode:
//   - non-empty: messages are load-balanced among subscribers in the same group
//   - empty: each subscriber receives every message (broadcast / fanout)
func (b *InMemoryEventBus) Subscribe(ctx context.Context, sub outbox.Subscription, handler outbox.EntryHandler) error {
	topic := sub.Topic
	consumerGroup := sub.ConsumerGroup

	subCtx, cancel := context.WithCancel(ctx)

	s := &subscription{
		ch:     make(chan outbox.Entry, b.bufSize),
		cancel: cancel,
		done:   make(chan struct{}),
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		cancel()
		return errcode.New(errcode.ErrBusClosed, "eventbus: bus is closed")
	}
	if b.groupSubs[topic] == nil {
		b.groupSubs[topic] = make(map[string]*groupState)
	}
	gs := b.groupSubs[topic][consumerGroup]
	if gs == nil {
		gs = &groupState{}
		b.groupSubs[topic][consumerGroup] = gs
	}
	gs.subs = append(gs.subs, s)
	b.mu.Unlock()

	// Signal readiness: close (or create+close) the per-subscription ready channel.
	b.signalReady(consumerGroup, topic)

	// Process messages in the current goroutine (Subscribe blocks per interface contract).
	defer func() {
		close(s.done)
		b.removeSub(topic, consumerGroup, s)
	}()
	for {
		select {
		case <-subCtx.Done():
			return subCtx.Err()
		case entry, ok := <-s.ch:
			if !ok {
				return nil
			}
			b.handleWithRetry(subCtx, topic, entry, handler)
		}
	}
}

// Close terminates all subscriber goroutines and prevents new publishes.
// Safety: Close holds mu.Lock() for the full channel-closing loop, while
// Publish holds mu.RLock() while sending to subscriber channels. That lock
// ordering prevents Publish from sending to a closed subscriber channel.
func (b *InMemoryEventBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil
	}
	b.closed = true

	for _, groups := range b.groupSubs {
		for _, gs := range groups {
			for _, sub := range gs.subs {
				sub.cancel()
				close(sub.ch)
			}
		}
	}
	return nil
}

// Health returns the current status of the event bus.
// Returns "healthy" when the bus is open, "closed" when it has been shut down.
func (b *InMemoryEventBus) Health() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return "closed"
	}
	return "healthy"
}

// DeadLetterLen returns the number of dead letter messages.
func (b *InMemoryEventBus) DeadLetterLen() int {
	b.deadLettersMu.Lock()
	defer b.deadLettersMu.Unlock()
	return len(b.deadLetters)
}

// DrainDeadLetters returns and clears all dead letter messages.
func (b *InMemoryEventBus) DrainDeadLetters() []DeadLetter {
	b.deadLettersMu.Lock()
	defer b.deadLettersMu.Unlock()
	dl := b.deadLetters
	b.deadLetters = nil
	return dl
}

// signalReady closes the per-subscription ready channel for the given
// consumerGroup + topic key. Safe to call multiple times (idempotent via sync.Once
// semantics implemented with the closed channel check).
func (b *InMemoryEventBus) signalReady(consumerGroup, topic string) {
	key := consumerGroup + "|" + topic
	b.readyMu.Lock()
	defer b.readyMu.Unlock()
	ch, ok := b.readyChans[key]
	if !ok {
		// Ready was never called; create an already-closed channel so future
		// calls to Ready(sub) return an immediately-closed channel.
		ch = make(chan struct{})
		b.readyChans[key] = ch
		close(ch)
		return
	}
	// Only close if still open (guard against double-close on re-subscribe).
	select {
	case <-ch:
		// already closed
	default:
		close(ch)
	}
}

// removeSub removes a specific subscription from the group's subscriber list.
// If the group becomes empty after removal, the groupState entry is pruned
// from the map to prevent unbounded growth.
func (b *InMemoryEventBus) removeSub(topic, consumerGroup string, target *subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	groups := b.groupSubs[topic]
	if groups == nil {
		return
	}
	gs := groups[consumerGroup]
	if gs == nil {
		return
	}
	for i, s := range gs.subs {
		if s == target {
			gs.subs = append(gs.subs[:i], gs.subs[i+1:]...)
			// Prune empty groupState to prevent map growth.
			if len(gs.subs) == 0 {
				delete(groups, consumerGroup)
				if len(groups) == 0 {
					delete(b.groupSubs, topic)
				}
			}
			return
		}
	}
}

func (b *InMemoryEventBus) handleWithRetry(ctx context.Context, topic string, entry outbox.Entry, handler outbox.EntryHandler) {
	var lastErr error
	for attempt := range maxRetries {
		res := handler(ctx, entry)
		done, err := b.processResult(ctx, topic, entry, res, attempt)
		if done {
			return
		}
		lastErr = err
		// Wait for retry delay or ctx cancellation.
		if !awaitRetry(ctx, res.Disposition, attempt) {
			return
		}
	}
	b.appendDeadLetter(topic, entry, lastErr)
	slog.Error("eventbus: retries exhausted, routing to dead letter",
		slog.String("topic", topic),
		slog.String("entry_id", entry.ID),
		slog.Any("error", lastErr),
	)
}

// processResult handles a single handler result. Returns (done=true) when
// no further retry is needed (Ack, Reject, or permanent error in Requeue).
// Returns the error to propagate as lastErr when done=false.
func (b *InMemoryEventBus) processResult(ctx context.Context, topic string, entry outbox.Entry, res outbox.HandleResult, attempt int) (done bool, lastErr error) {
	switch res.Disposition {
	case outbox.DispositionAck:
		if res.Receipt != nil {
			commitReceipt(ctx, res.Receipt, topic, entry.ID)
		}
		return true, nil
	case outbox.DispositionReject:
		if res.Receipt != nil {
			releaseReceipt(ctx, res.Receipt, topic, entry.ID)
		}
		slog.Warn("eventbus: handler rejected message, routing to dead letter",
			slog.String("topic", topic),
			slog.String("entry_id", entry.ID),
			slog.Any("error", res.Err),
		)
		b.appendDeadLetter(topic, entry, res.Err)
		return true, nil
	case outbox.DispositionRequeue:
		return b.handleRequeue(ctx, topic, entry, res, attempt)
	default:
		return b.handleInvalidDisposition(ctx, topic, entry, res, attempt)
	}
}

// handleRequeue processes DispositionRequeue: upgrades to dead letter on
// PermanentError, otherwise schedules retry with backoff.
func (b *InMemoryEventBus) handleRequeue(ctx context.Context, topic string, entry outbox.Entry, res outbox.HandleResult, attempt int) (done bool, lastErr error) {
	if res.Receipt != nil {
		releaseReceipt(ctx, res.Receipt, topic, entry.ID)
	}
	var permErr *outbox.PermanentError
	if res.Err != nil && errors.As(res.Err, &permErr) {
		slog.Warn("eventbus: permanent error in requeue, routing to dead letter",
			slog.String("topic", topic),
			slog.String("entry_id", entry.ID),
			slog.Any("error", res.Err),
		)
		b.appendDeadLetter(topic, entry, res.Err)
		return true, nil
	}
	delay := retryDelay(attempt)
	slog.Warn("eventbus: handler requested requeue, retrying",
		slog.String("topic", topic),
		slog.Int("attempt", attempt+1),
		slog.Any("error", res.Err),
		slog.Duration("retry_delay", delay),
	)
	return false, res.Err
}

// handleInvalidDisposition treats zero-value or unknown Disposition as Requeue
// with an Error-level log so the programming mistake is surfaced.
func (b *InMemoryEventBus) handleInvalidDisposition(ctx context.Context, topic string, entry outbox.Entry, res outbox.HandleResult, attempt int) (done bool, lastErr error) {
	if res.Receipt != nil {
		releaseReceipt(ctx, res.Receipt, topic, entry.ID)
	}
	delay := retryDelay(attempt)
	slog.Error("eventbus: invalid disposition, treating as requeue",
		slog.String("topic", topic),
		slog.String("entry_id", entry.ID),
		slog.String("disposition", res.Disposition.String()),
		slog.Int("attempt", attempt+1),
		slog.Duration("retry_delay", delay),
	)
	return false, res.Err
}

// retryDelay calculates exponential backoff with jitter for the given attempt.
// Delegates to outbox.ExponentialDelay for overflow-safe computation, capped at maxRetryDelay.
func retryDelay(attempt int) time.Duration {
	base := outbox.ExponentialDelay(baseRetryDelay, maxRetryDelay, attempt)
	jitter := time.Duration(rand.Int64N(int64(baseRetryDelay)))
	return base + jitter
}

// awaitRetry sleeps for the retry delay then returns true, or returns false
// if ctx is cancelled. For invalid disposition, uses the same delay logic.
func awaitRetry(ctx context.Context, _ outbox.Disposition, attempt int) bool {
	delay := retryDelay(attempt)
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

// appendDeadLetter records an entry in the dead letter queue.
func (b *InMemoryEventBus) appendDeadLetter(topic string, entry outbox.Entry, err error) {
	b.deadLettersMu.Lock()
	b.deadLetters = append(b.deadLetters, DeadLetter{
		Topic:   topic,
		Entry:   entry,
		LastErr: err,
	})
	b.deadLettersMu.Unlock()
}

// commitReceipt calls Receipt.Commit with a detached 5s-timeout context,
// consistent with the RabbitMQ subscriber path.
func commitReceipt(ctx context.Context, r outbox.Receipt, topic, entryID string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.Commit(rctx); err != nil {
		slog.Error("eventbus: receipt commit failed",
			slog.String("topic", topic),
			slog.String("entry_id", entryID),
			slog.String("error", err.Error()))
	}
}

// releaseReceipt calls Receipt.Release with a detached 5s-timeout context.
func releaseReceipt(ctx context.Context, r outbox.Receipt, topic, entryID string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.Release(rctx); err != nil {
		slog.Error("eventbus: receipt release failed",
			slog.String("topic", topic),
			slog.String("entry_id", entryID),
			slog.String("error", err.Error()))
	}
}

// ---------------------------------------------------------------------------
// Wire envelope unwrap
// ---------------------------------------------------------------------------

// unmarshalInboundEntry constructs an outbox.Entry for delivery to subscribers.
// Delegates to outboxrt.UnmarshalEnvelope which handles both relay wire envelopes
// and the raw-payload fallback (direct-publish semantics).
func unmarshalInboundEntry(topic string, payload []byte) outbox.Entry {
	entry, _ := outboxrt.UnmarshalEnvelope(topic, payload)
	return entry
}
