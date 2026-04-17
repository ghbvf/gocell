package assembly

import "testing"

// newTestAssembly is the canonical test-side constructor for CoreAssembly.
// It wraps New(cfg) and registers `t.Cleanup(a.Shutdown)` so every test
// that builds an assembly automatically drains the hook dispatcher
// goroutine at test teardown. goleak would otherwise flag the
// dispatcher's run function as leaking; pairing construction with
// cleanup is the contract we want every test to honour.
//
// ref: go.uber.org/goleak best practice — prefer `t.Cleanup(teardown)`
// over `defer teardown()` so leaks from sub-tests bubble up cleanly.
func newTestAssembly(t *testing.T, cfg Config) *CoreAssembly {
	t.Helper()
	a := New(cfg)
	t.Cleanup(a.Shutdown)
	return a
}
