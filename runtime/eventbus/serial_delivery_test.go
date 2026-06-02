package eventbus

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestInMemoryEventBus_GuaranteesSerialInOrderDelivery asserts that the
// in-memory bus implements outbox.SerialInOrderGuarantor and returns true: it
// consumes a single subscription's stream in one goroutine (FIFO), the serial
// in-order delivery a projection subscription requires (ADR §6 row 4).
//
// The runtime type assertion (rather than a compile-time var _) is intentional:
// it lets this test express the behavioral RED precisely — before the method
// exists, ok is false and the assertion fails, rather than failing to build.
//
// This unit test verifies the behavioral contract for one transport. The frozen
// method set and the exact implementer set {InMemoryEventBus} are locked by
// archtest PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01 (sub-rules A + B): any
// additional implementer fails that archtest in CI.
func TestInMemoryEventBus_GuaranteesSerialInOrderDelivery(t *testing.T) {
	t.Parallel()
	bus := New(clock.Real())

	g, ok := interface{}(bus).(outbox.SerialInOrderGuarantor)
	require.True(t, ok,
		"InMemoryEventBus must implement outbox.SerialInOrderGuarantor "+
			"(single-goroutine FIFO consume per subscription)")
	assert.True(t, g.GuaranteesSerialInOrderDelivery(),
		"InMemoryEventBus must report serial in-order delivery = true")
}
