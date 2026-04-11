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
	"log/slog"
	"math/rand/v2"
	"sync"
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
type InMemoryEventBus struct {
	mu            sync.RWMutex
	subs          map[string][]*subscription
	bufSize       int
	closed        bool
	deadLettersMu sync.Mutex
	deadLetters   []DeadLetter
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
		subs:    make(map[string][]*subscription),
		bufSize: 256,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Publish sends payload to all subscribers of the given topic.
// Non-blocking: if a subscriber's buffer is full, the message is dropped
// (logged as warning).
func (b *InMemoryEventBus) Publish(_ context.Context, topic string, payload []byte) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return errcode.New(errcode.ErrBusClosed, "eventbus: bus is closed")
	}

	entry := outbox.Entry{
		ID:        "evt" + "-" + uuid.NewString(),
		EventType: topic,
		Payload:   payload,
		CreatedAt: time.Now(),
	}

	for _, sub := range b.subs[topic] {
		select {
		case sub.ch <- entry:
		default:
			slog.Warn("eventbus: subscriber buffer full, message dropped",
				slog.String("topic", topic),
			)
		}
	}
	return nil
}

// Subscribe registers an EntryHandler for the given topic. It blocks until ctx
// is cancelled or the bus is closed.
//
// Consumer: cg-eventbus-{topic}
// Idempotency key: N/A (in-memory, no persistence)
// ACK timing: after handler returns DispositionAck
// Retry: transient errors -> retry 3x with exponential backoff / permanent -> dead letter
func (b *InMemoryEventBus) Subscribe(ctx context.Context, topic string, handler outbox.EntryHandler) error {
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
	b.subs[topic] = append(b.subs[topic], sub)
	b.mu.Unlock()

	// Process messages in the current goroutine (Subscribe blocks per interface contract).
	defer func() {
		close(sub.done)
		b.removeSub(topic, sub)
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
func (b *InMemoryEventBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil
	}
	b.closed = true

	for _, subs := range b.subs {
		for _, sub := range subs {
			sub.cancel()
			close(sub.ch)
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

// removeSub removes a specific subscription from the topic's subscriber list.
func (b *InMemoryEventBus) removeSub(topic string, target *subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := b.subs[topic]
	for i, s := range subs {
		if s == target {
			b.subs[topic] = append(subs[:i], subs[i+1:]...)
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
			return // success or safe duplicate
		case outbox.DispositionReject:
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
