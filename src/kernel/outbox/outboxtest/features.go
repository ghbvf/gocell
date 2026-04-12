package outboxtest

import "testing"

// Features declares which capabilities the implementation under test supports.
// Tests that require unsupported features are skipped with t.Skip().
//
// ref: ThreeDotsLabs/watermill pubsub/tests — Features struct pattern.
type Features struct {
	// Persistent means messages survive process restart.
	Persistent bool

	// GuaranteedOrder means messages on a single topic arrive in publish order.
	GuaranteedOrder bool

	// ExactlyOnceDelivery means the implementation deduplicates at the broker level.
	ExactlyOnceDelivery bool

	// SupportsRequeue means DispositionRequeue causes redelivery.
	SupportsRequeue bool

	// SupportsReject means DispositionReject routes to dead letter (no retry).
	SupportsReject bool

	// SupportsReceipt means the implementation threads Receipt through HandleResult.
	SupportsReceipt bool

	// SupportsMetadata means Entry.Metadata survives pub/sub round-trip.
	SupportsMetadata bool

	// BlockingSubscribe means Subscribe blocks until ctx is cancelled.
	BlockingSubscribe bool

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
