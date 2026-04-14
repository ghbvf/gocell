package eventbus_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// TestInMemoryEventBus_Conformance runs the full outboxtest conformance suite
// against InMemoryEventBus, proving the test helpers work.
func TestInMemoryEventBus_Conformance(t *testing.T) {
	outboxtest.TestPubSub(t, outboxtest.Features{
		GuaranteedOrder:   true,
		SupportsRequeue:   true,
		SupportsReject:    true,
		SupportsReceipt:   true,
		BlockingSubscribe: true,
	}, func(t *testing.T) (outbox.Publisher, outbox.Subscriber) {
		bus := eventbus.New(eventbus.WithBufferSize(256))
		t.Cleanup(func() { _ = bus.Close() })
		return bus, bus
	})
}
