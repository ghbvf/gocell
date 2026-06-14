package assembly

import (
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/clock"
)

// newTestAssembly is the canonical test-side constructor for CoreAssembly.
// It wraps New(clk, cfg) and registers `t.Cleanup(a.Shutdown)` so every test
// that builds an assembly automatically drains the hook dispatcher
// goroutine at test teardown. goleak would otherwise flag the
// dispatcher's run function as leaking; pairing construction with
// cleanup is the contract we want every test to honor.
//
// ref: go.uber.org/goleak best practice — prefer `t.Cleanup(teardown)`
// over `defer teardown()` so leaks from sub-tests bubble up cleanly.
func newTestAssembly(t *testing.T, clk clock.Clock, cfg Config) *CoreAssembly {
	t.Helper()
	a := New(clk, cfg)
	t.Cleanup(a.Shutdown)
	return a
}
