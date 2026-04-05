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
	ID            string
	AggregateID   string
	AggregateType string
	EventType     string
	Payload       []byte
	CreatedAt     time.Time
	Metadata      map[string]string // optional metadata (ref: Watermill Message.Metadata)
}

// Writer writes outbox entries within a transaction.
// The implementation must ensure the outbox write is atomic with the
// business state write (same DB transaction).
type Writer interface {
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
