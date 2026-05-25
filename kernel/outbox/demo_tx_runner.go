package outbox

import (
	"context"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// DemoTxRunner is the cell-boundary pass-through TxRunner installed at Cell
// Init() when the composition root has not provided a real persistence.TxRunner
// (publisher-only demo assemblies). It implements Nooper, so CheckNotNoop
// rejects it under DurabilityDurable mode — demo callers that forget to wire
// a real TxRunner surface an error at Init() time instead of silently losing
// cellvocab.L2 atomicity guarantees.
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
	ctx, drainAfterCommit := persistence.WithAfterCommitRegistry(ctx)
	mark := persistence.AfterCommitMark(ctx)
	if err := fn(ctx); err != nil {
		persistence.TruncateAfterCommitTo(ctx, mark) // discard this scope's hooks
		return err
	}
	if drainAfterCommit {
		persistence.RunAfterCommitHooks(ctx)
	}
	return nil
}

// DemoCellTxManager returns a sealed persistence.CellTxManager backed by
// DemoTxRunner. Cell.Init uses this when the composition root has not
// provided a real CellTxManager (publisher-only demo assemblies).
//
// The returned value still implements Nooper (via the wrapper's transparent
// Noop pass-through), so outbox.CheckNotNoop rejects it under
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

// DemoCellEmitter returns a sealed CellEmitter backed by NewNoopEmitter (a
// NoopWriter-backed WriterEmitter). It is the emitter-leg analog of
// DemoCellTxManager: composition roots and test builders that previously
// passed a raw outbox.NewNoopEmitter() to a cell/slice WithEmitter now pass
// DemoCellEmitter() so the public Option accepts the sealed CellEmitter.
//
// The wrapped emitter is non-durable (Durable()==false via NoopWriter), so a
// durable assembly that forgets a real emitter still surfaces the error at
// ResolveCellEmitter rather than silently losing L2 atomicity.
//
// The wrap call is restricted to this file by archtest
// CELL-RAW-INFRA-WRAPPER-LOCATION-01.
func DemoCellEmitter() CellEmitter {
	return WrapEmitterForCell(NewNoopEmitter())
}

// NewDirectCellEmitter returns a sealed CellEmitter backed by a DirectEmitter,
// for cells that publish directly with no transactional outbox by design —
// e.g. L4 DeviceLatent cells (KG-07: no outbox writer / txRunner). It composes
// NewDirectEmitter + WrapEmitterForCell so a public WithEmitter Option receives
// a sealed CellEmitter.
//
// Use this instead of ResolveCellEmitter for L4 direct-publish: unlike
// ResolveCellEmitter it carries NO cellvocab.L2 non-durable atomicity Warn.
// For L4 direct publish the absence of an outbox writer/txRunner is the
// intended production mode, not a demo degradation, so the L2 warning would be
// misleading. This is the "typed function choice" sibling of ResolveCellEmitter
// (mode-driven L1/L2/L3 resolution): the direct-publish-by-design semantic is
// expressed by the API name, not by a runtime suppression flag.
//
// The durable-publisher guard (outbox.CheckNotNoop) is the caller's
// responsibility before this path; this constructor does not re-derive
// durability mode. The wrapped DirectEmitter is non-durable (Durable()==false)
// and forwards its fail-open Probes() through the sealed wrapper.
//
// The wrap call is restricted to this file by archtest
// CELL-RAW-INFRA-WRAPPER-LOCATION-01.
func NewDirectCellEmitter(
	p Publisher, mode DirectPublishFailureMode, mp metrics.Provider, clk clock.Clock, cellID string, opts ...DirectEmitterOption,
) (CellEmitter, error) {
	de, err := NewDirectEmitter(p, mode, mp, clk, cellID, opts...)
	if err != nil {
		return nil, err
	}
	return WrapEmitterForCell(de), nil
}
