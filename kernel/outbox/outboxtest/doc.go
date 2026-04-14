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
// ref: ThreeDotsLabs/watermill pubsub/tests/test_pubsub.go — universal
// conformance suite pattern (Features flags + constructor injection).
package outboxtest
