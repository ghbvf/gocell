// Package outboxtest provides a reusable conformance test suite for
// outbox.Publisher and outbox.Subscriber implementations.
//
// Usage: call TestPubSub in an implementation's _test.go file with a
// PubSubConstructor that creates the Publisher/Subscriber under test
// and a Features struct declaring the implementation's capabilities.
//
//	func TestMyBroker_Conformance(t *testing.T) {
//	    outboxtest.TestPubSub(t, outboxtest.Features{
//	        SupportsRequeue: true,
//	        SupportsReject:  true,
//	    }, func(t *testing.T) (outbox.Publisher, outbox.Subscriber) {
//	        bus := mybroker.New()
//	        t.Cleanup(func() { _ = bus.Close() })
//	        return bus, bus
//	    })
//	}
//
// # Close convention
//
// All Subscriber.Close call sites inside the conformance suite must route
// through the package-internal closeWithBudget helper (defined in helpers.go),
// never via a bare sub.Close(ctx). closeWithBudget wraps Close with a
// caller-side timeout budget and goroutine-leak detection, turning a
// misbehaving implementation into a focused per-test failure rather than a
// process-level hang. PubSubConstructor t.Cleanup callbacks that close the
// full bus object are exempt — their lifetime is managed by the test framework.
//
// ref: ThreeDotsLabs/watermill pubsub/tests/test_pubsub.go — universal
// conformance suite pattern (Features flags + constructor injection).
package outboxtest
