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

	// CellID is the observability owner label. Required.
	//
	// CellID is the single source of truth for the cell owning this
	// subscription — metrics, slog fields, and trace span attributes use it
	// to route subscriber telemetry to the right owner. The value is set by
	// codegen (contractgen NewSubscription / cellgen cell.tmpl) from the
	// cell's metadata.ID at compile time; there is no runtime fallback to
	// ConsumerGroup. ConsumerGroup is a broker partition key + idempotency
	// namespace, conceptually orthogonal to ownership.
	//
	// ref: ADR docs/architecture/202605111000-adr-subscription-cellid-mandatory.md
	CellID string

	// SliceID is an optional observability owner label. Codegen fills it
	// from +slice:subscribe markers when a cell wants per-slice consumer
	// metrics.
	SliceID string

	// ContractID/Kind/Transport identify the contract bound to this
	// subscription. They are intentionally primitive strings rather than a
	// contractspec.ContractSpec to keep kernel/outbox independent of
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
//
// Subscription.Validate is the SINGLE source of truth for subscription-shape
// invariants. Subscribe-time decorators (e.g. ContractTracingSubscriber) call
// it once at the entry point instead of duplicating the checks downstream
// (N8 (c) — collapsed the previous parallel check inside MustWrapSubscriber).
func (s Subscription) Validate() error {
	if s.Topic == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox: subscription Topic must not be empty")
	}
	if s.ConsumerGroup == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox: subscription ConsumerGroup must not be empty")
	}
	if s.CellID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox: subscription CellID must not be empty")
	}
	if s.ContractID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox: subscription ContractID must not be empty")
	}
	if s.ContractKind == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox: subscription ContractKind must not be empty")
	}
	if s.ContractTransport == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox: subscription ContractTransport must not be empty")
	}
	return nil
}

// IdempotencyNamespace returns the namespace prefix for idempotency keys.
// Keys are constructed as "{IdempotencyNamespace}:{entry.ID}".
func (s Subscription) IdempotencyNamespace() string {
	return s.ConsumerGroup
}

// ObservabilityID returns the human-readable identifier used in logs and metrics.
//
// Returns CellID unconditionally — Validate enforces CellID is non-empty, so
// the empty string only surfaces when the caller bypassed Validate (e.g.
// constructing a literal in a test fixture). There is no fallback to
// ConsumerGroup: substituting ConsumerGroup would mask a codegen defect (the
// cell metadata → reg.Subscribe positional parameter chain failed to populate
// the field), so the empty value is intentionally surfaced.
//
// ref: ADR docs/architecture/202605111000-adr-subscription-cellid-mandatory.md
func (s Subscription) ObservabilityID() string {
	return s.CellID
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
