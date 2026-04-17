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
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/google/uuid"
)

const (
	maxRetries     = 3
	baseRetryDelay = 100 * time.Millisecond

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
		groupSubs: make(map[string]map[string]*groupState),
		bufSize:   256,
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

	groups := b.groupSubs[topic]
	for group, gs := range groups {
		if len(gs.subs) == 0 {
			continue
		}
		if group == "" {
			// Empty group → broadcast to all subscribers (backward compatible).
			for _, sub := range gs.subs {
				select {
				case sub.ch <- entry:
				default:
					slog.Warn("eventbus: subscriber buffer full, message dropped",
						slog.String("topic", topic),
					)
				}
			}
		} else {
			// Named group → round-robin to one subscriber (competing).
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
	}
	return nil
}

// Subscribe registers an EntryHandler for the given topic. It blocks until ctx
// is cancelled or the bus is closed.
//
// Consumer: cg-eventbus-{consumerGroup}-{topic}
// Idempotency key: N/A (in-memory, no persistence)
// ACK timing: after handler returns DispositionAck
// Retry: transient errors -> retry 3x with exponential backoff / permanent -> dead letter
//
// consumerGroup selects the dispatch mode:
//   - non-empty: messages are load-balanced among subscribers in the same group
//   - empty: each subscriber receives every message (broadcast / fanout)
func (b *InMemoryEventBus) Subscribe(ctx context.Context, topic string, handler outbox.EntryHandler, consumerGroup string) error {
	subCtx, cancel := context.WithCancel(ctx)

	sub := &subscription{
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
	gs.subs = append(gs.subs, sub)
	b.mu.Unlock()

	// Process messages in the current goroutine (Subscribe blocks per interface contract).
	defer func() {
		close(sub.done)
		b.removeSub(topic, consumerGroup, sub)
	}()
	for {
		select {
		case <-subCtx.Done():
			return subCtx.Err()
		case entry, ok := <-sub.ch:
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
		switch res.Disposition {
		case outbox.DispositionAck:
			if res.Receipt != nil {
				commitReceipt(ctx, res.Receipt, topic, entry.ID)
			}
			return // success or safe duplicate
		case outbox.DispositionReject:
			if res.Receipt != nil {
				releaseReceipt(ctx, res.Receipt, topic, entry.ID)
			}
			// Permanent failure — route directly to dead letter.
			slog.Warn("eventbus: handler rejected message, routing to dead letter",
				slog.String("topic", topic),
				slog.String("entry_id", entry.ID),
				slog.Any("error", res.Err),
			)
			b.deadLettersMu.Lock()
			b.deadLetters = append(b.deadLetters, DeadLetter{
				Topic:   topic,
				Entry:   entry,
				LastErr: res.Err,
			})
			b.deadLettersMu.Unlock()
			return
		case outbox.DispositionRequeue:
			if res.Receipt != nil {
				releaseReceipt(ctx, res.Receipt, topic, entry.ID)
			}
			// PermanentError in Requeue → upgrade to dead letter (no retry).
			// Mirrors ConsumerBase behavior: PermanentError takes precedence
			// over the Disposition, ensuring consistent routing regardless of
			// whether the handler or WrapLegacyHandler set the Disposition.
			var permErr *outbox.PermanentError
			if res.Err != nil && errors.As(res.Err, &permErr) {
				slog.Warn("eventbus: permanent error in requeue, routing to dead letter",
					slog.String("topic", topic),
					slog.String("entry_id", entry.ID),
					slog.Any("error", res.Err),
				)
				b.deadLettersMu.Lock()
				b.deadLetters = append(b.deadLetters, DeadLetter{
					Topic:   topic,
					Entry:   entry,
					LastErr: res.Err,
				})
				b.deadLettersMu.Unlock()
				return
			}
			lastErr = res.Err
			jitter := time.Duration(rand.Int64N(int64(baseRetryDelay)))
			delay := baseRetryDelay*(1<<attempt) + jitter // e.g. 100-200ms, 200-300ms, 400-500ms
			slog.Warn("eventbus: handler requested requeue, retrying",
				slog.String("topic", topic),
				slog.Int("attempt", attempt+1),
				slog.Any("error", res.Err),
				slog.Duration("retry_delay", delay),
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
				continue
			}
		default:
			// Zero-value or unknown Disposition — treat as requeue (safe degradation)
			// and log at Error level so the programming mistake is surfaced.
			if res.Receipt != nil {
				releaseReceipt(ctx, res.Receipt, topic, entry.ID)
			}
			lastErr = res.Err
			jitter := time.Duration(rand.Int64N(int64(baseRetryDelay)))
			delay := baseRetryDelay*(1<<attempt) + jitter
			slog.Error("eventbus: invalid disposition, treating as requeue",
				slog.String("topic", topic),
				slog.String("entry_id", entry.ID),
				slog.String("disposition", res.Disposition.String()),
				slog.Int("attempt", attempt+1),
				slog.Duration("retry_delay", delay),
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
				continue
			}
		}
	}

	// Exhausted retries → dead letter.
	slog.Error("eventbus: retries exhausted, routing to dead letter",
		slog.String("topic", topic),
		slog.String("entry_id", entry.ID),
		slog.Any("error", lastErr),
	)
	b.deadLettersMu.Lock()
	b.deadLetters = append(b.deadLetters, DeadLetter{
		Topic:   topic,
		Entry:   entry,
		LastErr: lastErr,
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

// outboxWireMessage mirrors the envelope produced by
// adapters/postgres/outbox_relay.go publishAll and consumed by
// adapters/rabbitmq/subscriber.go unmarshalDelivery. Keeping the struct here
// lets the in-memory bus treat relay output symmetrically with the broker
// path, closing the F1 contract asymmetry identified in PR#174 review.
//
// When OUTBOX-ENVELOPE-KERNEL-SHARE-01 lands, this struct + detection move
// to kernel/outbox and both transports depend on a single source of truth.
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

// unmarshalInboundEntry constructs an outbox.Entry for delivery to subscribers.
// If payload is a relay envelope it is unwrapped; otherwise the raw payload
// is wrapped in a freshly stamped Entry (preserves the pre-envelope direct-
// publish semantics).
//
// Discriminator mirrors adapters/rabbitmq/subscriber.go: require non-empty
// ID + EventType and payload that starts with '{'/'[' so business payloads
// happening to parse as an envelope (unlikely but possible) do not flip the
// detection.
func unmarshalInboundEntry(topic string, payload []byte) outbox.Entry {
	var msg outboxWireMessage
	if err := json.Unmarshal(payload, &msg); err == nil &&
		msg.ID != "" && msg.EventType != "" && isEmbeddedJSON(msg.Payload) {
		return outbox.Entry{
			ID:            msg.ID,
			AggregateID:   msg.AggregateID,
			AggregateType: msg.AggregateType,
			EventType:     msg.EventType,
			Topic:         msg.Topic,
			Payload:       []byte(msg.Payload),
			Metadata:      msg.Metadata,
			CreatedAt:     msg.CreatedAt,
		}
	}
	return outbox.Entry{
		ID:        "evt-" + uuid.NewString(),
		EventType: topic,
		Payload:   payload,
		CreatedAt: time.Now(),
	}
}

// isEmbeddedJSON returns true if the raw JSON value is an object or array
// (relay envelope payload), not a base64 string or primitive.
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
