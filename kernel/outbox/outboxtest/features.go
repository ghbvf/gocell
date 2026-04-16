package outboxtest

import "testing"

// Features declares which capabilities the implementation under test supports.
// Tests that require unsupported features are skipped with t.Skip().
//
// Only capabilities that the conformance suite can verify with executable
// assertions are included. Capabilities that require adapter-specific
// mechanisms (e.g., metadata round-trip, persistence, exactly-once delivery)
// should be tested in the adapter's own test files, not via this struct.
//
// ref: ThreeDotsLabs/watermill pubsub/tests — Features struct pattern.
type Features struct {
	// GuaranteedOrder means messages on a single topic arrive in publish order.
	GuaranteedOrder bool

	// SupportsRequeue means DispositionRequeue causes redelivery.
	SupportsRequeue bool

	// SupportsReject means DispositionReject routes to dead letter (no retry).
	SupportsReject bool

	// SupportsReceipt means the implementation threads Receipt through HandleResult.
	SupportsReceipt bool

	// BlockingSubscribe means Subscribe blocks until ctx is cancelled.
	BlockingSubscribe bool

	// BroadcastSubscribe indicates that multiple Subscribe calls on the same
	// topic from the same Subscriber instance deliver a copy to EVERY handler
	// (fan-out). When false, multiple subscribers on the same topic compete
	// for messages (round-robin); testMultipleSubscribers is skipped.
	// InMemoryEventBus: true. RabbitMQ (same queue without explicit group): false.
	BroadcastSubscribe bool

	// MessageCount is how many messages to use in bulk tests.
	// Default: 100 (short mode: 10).
	MessageCount int
}

// setDefaults fills zero-value fields with sensible defaults.
func (f *Features) setDefaults() {
	if f.MessageCount == 0 {
		if testing.Short() {
			f.MessageCount = 10
		} else {
			f.MessageCount = 100
		}
	}
}
