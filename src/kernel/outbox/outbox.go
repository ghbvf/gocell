// Package outbox defines interfaces for the transactional outbox pattern.
// Implementations live in adapters/ (e.g., adapters/postgres).
//
// ref: ThreeDotsLabs/watermill message/ — Message 統一模型, Publisher/Subscriber 接口
package outbox

import (
	"context"
	"time"
)

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

// Subscriber consumes events from a topic.
//
// ref: ThreeDotsLabs/watermill message/pubsub.go Subscriber interface
// Adopted: Close() for clean shutdown; topic-based subscription model.
// Deviated: callback-based handler instead of channel-based (<-chan *Message)
// to align with GoCell's ConsumerBase pattern and simplify consumer lifecycle.
type Subscriber interface {
	// Subscribe registers a handler for the given topic. The handler is called
	// for each incoming entry. Returning a non-nil error from the handler
	// signals a transient failure (retry/NACK); permanent failures should be
	// routed to a dead-letter queue by the implementation.
	//
	// Subscribe blocks until ctx is cancelled or an unrecoverable error occurs.
	Subscribe(ctx context.Context, topic string, handler func(context.Context, Entry) error) error

	// Close terminates all active subscriptions and releases resources.
	Close() error
}

// TopicHandlerMiddleware transforms an entry handler, receiving the topic name.
// It is the event-consumer analogue of HTTP middleware.
type TopicHandlerMiddleware func(topic string, next func(context.Context, Entry) error) func(context.Context, Entry) error

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
func (s *SubscriberWithMiddleware) Subscribe(ctx context.Context, topic string, handler func(context.Context, Entry) error) error {
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
