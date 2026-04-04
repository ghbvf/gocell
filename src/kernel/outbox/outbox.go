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
