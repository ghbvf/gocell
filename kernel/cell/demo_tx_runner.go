// Package cell provides the Cell/Slice runtime and governance primitives.
package cell

import (
	"context"

	"github.com/ghbvf/gocell/kernel/persistence"
)

// DemoTxRunner is the cell-boundary pass-through TxRunner installed at Cell
// Init() when the composition root has not provided a real persistence.TxRunner
// (publisher-only demo assemblies). It implements Nooper, so CheckNotNoop
// rejects it under DurabilityDurable mode — demo callers that forget to wire
// a real TxRunner surface an error at Init() time instead of silently losing
// L2 atomicity guarantees.
type DemoTxRunner struct{}

// Compile-time assertion: DemoTxRunner must satisfy Nooper.
var _ Nooper = DemoTxRunner{}

// Noop reports DemoTxRunner as a no-op runner for CheckNotNoop guards.
func (DemoTxRunner) Noop() bool { return true }

// RunInTx executes fn directly without a real transaction wrapper.
// nil fn is treated as a no-op for safety.
func (DemoTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

// DemoCellTxManager returns a sealed persistence.CellTxManager backed by
// DemoTxRunner. Cell.Init uses this when the composition root has not
// provided a real CellTxManager (publisher-only demo assemblies).
//
// The returned value still implements Nooper (via the wrapper's transparent
// Noop pass-through), so cell.CheckNotNoop rejects it under
// DurabilityDurable — demo fallbacks can never silently slip into a durable
// assembly.
//
// This factory is the kernel-internal demo entry point; it pairs with
// composition-root wraps (persistence.WrapForCell) for production wiring.
// The wrap call is restricted to this file by archtest
// CELL-RAW-INFRA-WRAPPER-LOCATION-01.
func DemoCellTxManager() persistence.CellTxManager {
	return persistence.WrapForCell(DemoTxRunner{})
}
