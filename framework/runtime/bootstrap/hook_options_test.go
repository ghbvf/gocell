package bootstrap

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/outbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
)

// TestValidateAssemblyClockAlignment_SameClock passes phase0 when the
// bootstrap clock and the pre-built assembly clock are the same instance.
func TestValidateAssemblyClockAlignment_SameClock(t *testing.T) {
	clk := clockmock.New(time.Now())
	asm := assembly.New(clk, assembly.Config{
		ID:             "test",
		DurabilityMode: outbox.DurabilityDemo,
	})
	b := &Bootstrap{
		clock:        clk,
		assemblyCore: asm,
	}
	err := b.validateAssemblyClockAlignment()
	require.NoError(t, err)
}

// TestValidateAssemblyClockAlignment_DifferentClocks fails phase0 with a
// descriptive error when the assembly clock and the bootstrap clock differ.
func TestValidateAssemblyClockAlignment_DifferentClocks(t *testing.T) {
	clkBootstrap := clockmock.New(time.Now())
	clkAssembly := clockmock.New(time.Now())
	asm := assembly.New(clkAssembly, assembly.Config{
		ID:             "test",
		DurabilityMode: outbox.DurabilityDemo,
	})
	b := &Bootstrap{
		clock:        clkBootstrap,
		assemblyCore: asm,
	}
	err := b.validateAssemblyClockAlignment()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clock mismatch")
	assert.Contains(t, err.Error(), "bootstrap.New(clk")
	assert.Contains(t, err.Error(), "assembly.New(clk")
}

// TestValidateAssemblyClockAlignment_NoAssembly passes phase0 when no
// pre-built assembly is set (bootstrap builds its own, always aligned).
func TestValidateAssemblyClockAlignment_NoAssembly(t *testing.T) {
	clk := clock.Real()
	b := &Bootstrap{
		clock:        clk,
		assemblyCore: nil,
	}
	err := b.validateAssemblyClockAlignment()
	require.NoError(t, err)
}
