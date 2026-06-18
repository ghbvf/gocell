// per_cell_broker_test.go: table-driven tests for LoadBrokerURL (per-cell broker
// URL env loading with assembly-wide fallback). Mirrors per_cell_adapter_test.go.
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestLoadBrokerURL covers the per-cell override, the assembly-wide fallback,
// the neither-set case, and cross-cell isolation. Uses t.Setenv (no t.Parallel).
func TestLoadBrokerURL(t *testing.T) {
	const (
		perCell  = "amqp://percell:5672/"
		fallback = "amqp://fallback:5672/"
	)

	t.Run("per-cell override wins over fallback", func(t *testing.T) {
		t.Setenv("GOCELL_CONFIGCORE_AMQP_URL", perCell)
		t.Setenv("GOCELL_AMQP_URL", fallback)
		assert.Equal(t, perCell, LoadBrokerURL("CONFIGCORE"),
			"a set per-cell URL must take precedence over the assembly-wide fallback")
	})

	t.Run("falls back to assembly-wide GOCELL_AMQP_URL when per-cell unset", func(t *testing.T) {
		t.Setenv("GOCELL_CONFIGCORE_AMQP_URL", "")
		t.Setenv("GOCELL_AMQP_URL", fallback)
		assert.Equal(t, fallback, LoadBrokerURL("CONFIGCORE"),
			"an unset per-cell URL must fall back to GOCELL_AMQP_URL")
	})

	t.Run("returns empty when neither is set", func(t *testing.T) {
		t.Setenv("GOCELL_CONFIGCORE_AMQP_URL", "")
		t.Setenv("GOCELL_AMQP_URL", "")
		assert.Equal(t, "", LoadBrokerURL("CONFIGCORE"),
			"with no per-cell URL and no fallback the helper returns empty (resolver fail-closes downstream)")
	})

	t.Run("cross-cell isolation: one cell's URL does not leak to another", func(t *testing.T) {
		t.Setenv("GOCELL_ACCESSCORE_AMQP_URL", perCell)
		t.Setenv("GOCELL_CONFIGCORE_AMQP_URL", "")
		t.Setenv("GOCELL_AMQP_URL", fallback)
		// accesscore reads its own per-cell override...
		assert.Equal(t, perCell, LoadBrokerURL("ACCESSCORE"))
		// ...while configcore (no per-cell var) still resolves the fallback, NOT
		// accesscore's per-cell value.
		assert.Equal(t, fallback, LoadBrokerURL("CONFIGCORE"),
			"a sibling cell's per-cell URL must not leak across the cell namespace")
	})
}
