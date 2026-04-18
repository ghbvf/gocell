package outbox

import "github.com/ghbvf/gocell/pkg/errcode"

// Subscription describes the full identity of a single subscription intent.
// It is the first-class object passed through the middleware chain, ensuring
// every layer (idempotency, observability, retry) sees Topic, ConsumerGroup,
// and CellID without information loss at middleware boundaries.
//
// ref: ThreeDotsLabs/watermill message/router.go handler context injection
// ref: MassTransit ConsumeContext — full identity traverses the entire pipeline
type Subscription struct {
	// Topic is the broker routing key. Required.
	Topic string

	// ConsumerGroup is the logical consumer group identity. Required.
	// It forms the idempotency key namespace: "{ConsumerGroup}:{entry.ID}".
	// Subscribers sharing the same ConsumerGroup compete for messages;
	// different groups each receive a full copy (fanout).
	ConsumerGroup string

	// CellID is an optional observability label. When set it is used in log
	// fields and metrics labels; falls back to ConsumerGroup when empty.
	CellID string
}

// Validate returns an error when required fields are missing.
func (s Subscription) Validate() error {
	if s.Topic == "" {
		return errcode.New(errcode.ErrValidationFailed, "outbox: subscription Topic must not be empty")
	}
	if s.ConsumerGroup == "" {
		return errcode.New(errcode.ErrValidationFailed, "outbox: subscription ConsumerGroup must not be empty")
	}
	return nil
}

// IdempotencyNamespace returns the namespace prefix for idempotency keys.
// Keys are constructed as "{IdempotencyNamespace}:{entry.ID}".
func (s Subscription) IdempotencyNamespace() string {
	return s.ConsumerGroup
}

// ObservabilityID returns the human-readable identifier used in logs and metrics.
// Prefers CellID when set; falls back to ConsumerGroup.
func (s Subscription) ObservabilityID() string {
	if s.CellID != "" {
		return s.CellID
	}
	return s.ConsumerGroup
}

// SubscriptionMiddleware is the event-consumer middleware type that carries
// the full Subscription identity. It replaces TopicHandlerMiddleware, which
// only passed the topic string and lost ConsumerGroup at middleware boundaries.
//
// Apply in order: [0] is outermost, [len-1] is innermost.
type SubscriptionMiddleware func(sub Subscription, next EntryHandler) EntryHandler
