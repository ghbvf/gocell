package outbox

import "github.com/ghbvf/gocell/pkg/errcode"

// Subscription describes the full identity of a single subscription intent.
// It is the first-class object passed through the middleware chain, ensuring
// every layer (idempotency, observability, retry) sees Topic, ConsumerGroup,
// CellID, and SliceID without information loss at middleware boundaries.
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

	// SliceID is an optional observability owner label. Runtime eventrouter
	// fills it from the subscription declaration when a cell wants per-slice
	// consumer metrics.
	SliceID string

	// ContractID/Kind/Transport identify the contract bound to this
	// subscription. They are intentionally primitive strings rather than a
	// wrapper.ContractSpec to keep kernel/outbox independent of
	// kernel/wrapper (wrapper already imports outbox for EntryHandler).
	//
	// Runtime eventrouter fills these fields for AddContractHandler
	// registrations so subscription middleware can install the contract span
	// outside ConsumerBase while still after observability metadata restore.
	ContractID        string
	ContractKind      string
	ContractTransport string
}

// Validate returns an error when required fields are missing.
func (s Subscription) Validate() error {
	if s.Topic == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox: subscription Topic must not be empty")
	}
	if s.ConsumerGroup == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox: subscription ConsumerGroup must not be empty")
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

// SubscriptionMiddleware is the event-consumer middleware type. It carries
// the full Subscription identity (Topic + ConsumerGroup + CellID), so middleware
// can route, log, and observe per-subscription rather than per-topic.
//
// Apply in order: [0] is outermost, [len-1] is innermost.
//
// The next handler is EntryHandler (business signature). Middleware authors work
// entirely in the business domain and do not see Settlement — it is an internal
// concern of the Subscriber layer (adapters/rabbitmq, runtime/eventbus).
// ConsumerBase is field-injected into SubscriberWithMiddleware and applied as
// an explicit EntryHandler→SubscriberHandler conversion boundary after the
// business middleware chain, not as a SubscriptionMiddleware entry.
//
// ref: ThreeDotsLabs/watermill message/router.go — HandlerMiddleware operates on
// Message→Message; Ack/Nack is the router's exclusive responsibility.
// ref: go-kratos/kratos middleware — middleware operates on Request→Reply;
// gRPC status (settle equivalent) is the transport's exclusive responsibility.
type SubscriptionMiddleware func(sub Subscription, next EntryHandler) EntryHandler
