// Package outbox defines interfaces for the transactional outbox pattern.
// Implementations live in adapters/ (e.g., adapters/postgres).
//
// ref: ThreeDotsLabs/watermill message/ — Message 統一模型, Publisher/Subscriber 接口
package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// Entry
// ---------------------------------------------------------------------------

// Entry represents a single outbox record to be published.
type Entry struct {
	// ID is the canonical idempotency identifier. Consumers SHOULD use this
	// field to construct idempotency keys.
	ID string
	AggregateID   string
	AggregateType string
	EventType     string
	Topic         string // broker routing key; falls back to EventType if empty
	Payload       []byte
	CreatedAt     time.Time
	Metadata      map[string]string // optional metadata (ref: Watermill Message.Metadata)
}

// RoutingTopic returns the broker routing key for the entry.
// If Topic is set, it is returned; otherwise EventType is used as fallback.
func (e Entry) RoutingTopic() string {
	if e.Topic != "" {
		return e.Topic
	}
	return e.EventType
}

// Validate checks that required fields (Topic or EventType, and Payload) are
// present. Writers SHOULD call Validate before persisting. (F-OB-03)
func (e Entry) Validate() error {
	if e.RoutingTopic() == "" {
		return errcode.New(errcode.ErrValidationFailed, "outbox: entry missing topic (Topic and EventType are both empty)")
	}
	if len(e.Payload) == 0 {
		return errcode.New(errcode.ErrValidationFailed, "outbox: entry missing payload")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Writer / Relay / Publisher
// ---------------------------------------------------------------------------

// Writer writes outbox entries within a transaction.
// The implementation must ensure the outbox write is atomic with the
// business state write (same DB transaction).
type Writer interface {
	// Write persists an outbox entry atomically with the caller's business state.
	// Implementations that require transactional guarantees SHOULD use a
	// context-embedded transaction pattern (e.g., extract tx from context via
	// TxFromContext(ctx)) to participate in the caller's transaction scope.
	Write(ctx context.Context, entry Entry) error
}

// Relay polls unpublished outbox entries and publishes them.
type Relay interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Publisher sends events to the message broker.
type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

// ---------------------------------------------------------------------------
// Disposition / Receipt / HandleResult — Solution B types
// ---------------------------------------------------------------------------

// Disposition describes the broker-level action for a consumed message.
//
//   - DispositionAck:     message processed successfully (or duplicate); broker may discard.
//   - DispositionRequeue: temporary failure / shutdown; broker should redeliver.
//   - DispositionReject:  permanent failure; broker routes to dead-letter exchange.
type Disposition uint8

const (
	DispositionAck     Disposition = iota // ACK — success or safe-to-skip duplicate
	DispositionRequeue                    // NACK+requeue — transient / shutdown
	DispositionReject                     // NACK+no-requeue — permanent failure → DLX
)

// String returns a human-readable label for the Disposition.
func (d Disposition) String() string {
	switch d {
	case DispositionAck:
		return "ack"
	case DispositionRequeue:
		return "requeue"
	case DispositionReject:
		return "reject"
	default:
		return fmt.Sprintf("disposition(%d)", d)
	}
}

// Receipt represents a claimable idempotency token attached to a single
// message processing attempt. The broker-layer (Subscriber) manages the
// Receipt lifecycle AFTER executing the broker Ack/Nack:
//
//   - DispositionAck    + broker Ack success  → Receipt.Commit()
//   - DispositionReject + broker Nack success → Receipt.Release()  (allows DLQ replay)
//   - DispositionRequeue + broker Nack success → Receipt.Release()
//   - Any broker Ack/Nack failure             → Receipt.Release()
//
// Callers MUST use context.WithoutCancel for Receipt operations to ensure
// idempotency state is persisted even during graceful shutdown.
type Receipt interface {
	// Commit marks the idempotency key as permanently done.
	// Only called after DispositionAck + successful broker Ack.
	Commit(ctx context.Context) error
	// Release removes the processing lease so redelivery can re-enter.
	// Called for Reject (allows DLQ replay), Requeue, and broker errors.
	Release(ctx context.Context) error
}

// HandleResult carries the business handler's processing outcome.
// The Subscriber inspects Disposition to decide Ack/Nack, then calls
// Receipt.Commit or Receipt.Release based on the broker outcome.
type HandleResult struct {
	Disposition Disposition
	Err         error   // optional: logged/observed; nil for success
	Receipt     Receipt // nil when idempotency is not in use
}

// EntryHandler is the Solution B handler signature. Business handlers return
// a HandleResult that declares the intended broker disposition and carries an
// optional idempotency Receipt.
type EntryHandler func(context.Context, Entry) HandleResult

// ---------------------------------------------------------------------------
// Legacy compatibility
// ---------------------------------------------------------------------------

// LegacyHandler is the pre-Solution-B handler signature kept for reference.
// New code should use EntryHandler.
type LegacyHandler = func(context.Context, Entry) error

// WrapLegacyHandler adapts a LegacyHandler to the new EntryHandler contract:
//   - nil error  → DispositionAck
//   - non-nil error → DispositionRequeue (transient by default)
//
// Note: PermanentError is mapped to DispositionRequeue, not DispositionReject.
// ConsumerBase.Wrap detects PermanentError via errors.As and upgrades to Reject.
// Without ConsumerBase wrapping, PermanentError will be retried like any other
// error. Direct Subscribe callers needing Reject should use EntryHandler directly.
//
// This allows existing cell handlers to compile against the new Subscriber
// interface without immediate rewrite.
func WrapLegacyHandler(fn LegacyHandler) EntryHandler {
	return func(ctx context.Context, entry Entry) HandleResult {
		if err := fn(ctx, entry); err != nil {
			return HandleResult{Disposition: DispositionRequeue, Err: err}
		}
		return HandleResult{Disposition: DispositionAck}
	}
}

// ---------------------------------------------------------------------------
// Subscriber
// ---------------------------------------------------------------------------

// Subscriber consumes events from a topic.
//
// ref: ThreeDotsLabs/watermill message/pubsub.go Subscriber interface
// Adopted: Close() for clean shutdown; topic-based subscription model.
// Deviated: callback-based EntryHandler instead of channel-based (<-chan *Message)
// to align with GoCell's ConsumerBase pattern and simplify consumer lifecycle.
type Subscriber interface {
	// Subscribe registers a handler for the given topic. The handler is called
	// for each incoming entry and returns a HandleResult that declares the
	// intended broker disposition.
	//
	// Subscribe blocks until ctx is cancelled or an unrecoverable error occurs.
	Subscribe(ctx context.Context, topic string, handler EntryHandler) error

	// Close terminates all active subscriptions and releases resources.
	Close() error
}

// TopicHandlerMiddleware transforms an EntryHandler, receiving the topic name.
// It is the event-consumer analogue of HTTP middleware.
type TopicHandlerMiddleware func(topic string, next EntryHandler) EntryHandler

// SubscriberWithMiddleware wraps a Subscriber so that every handler passed
// to Subscribe is first wrapped by the given middleware chain.
// Middleware is applied in order: [0] is outermost, [len-1] is innermost.
type SubscriberWithMiddleware struct {
	Inner      Subscriber
	Middleware []TopicHandlerMiddleware
}

// Compile-time interface check.
var _ Subscriber = (*SubscriberWithMiddleware)(nil)

// Subscribe wraps the handler with the middleware chain, then delegates to Inner.
func (s *SubscriberWithMiddleware) Subscribe(ctx context.Context, topic string, handler EntryHandler) error {
	wrapped := handler
	for i := len(s.Middleware) - 1; i >= 0; i-- {
		wrapped = s.Middleware[i](topic, wrapped)
	}
	return s.Inner.Subscribe(ctx, topic, wrapped)
}

// Close delegates to the inner subscriber.
func (s *SubscriberWithMiddleware) Close() error {
	return s.Inner.Close()
}
