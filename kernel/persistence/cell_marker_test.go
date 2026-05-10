package persistence_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/persistence"
)

type fakeTxRunner struct{ runs int }

func (f *fakeTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	f.runs++
	return fn(ctx)
}

// TestCellTxManager_WrapNilReturnsNil keeps caller-side typed-nil detection
// working: builder options consume `if pub != nil` semantics, so the wrapper
// must collapse a nil TxRunner into a true nil interface (not a typed-nil
// internalCellTxManager).
func TestCellTxManager_WrapNilReturnsNil(t *testing.T) {
	t.Parallel()
	if persistence.WrapForCell(nil) != nil {
		t.Fatal("WrapForCell(nil) must return nil interface")
	}
}

// TestCellTxManager_WrapTypedNilReturnsNil pins the typed-nil interface
// guard. Composition roots routinely write `var p *postgres.TxManager` and
// pass `p` (which becomes a typed-nil interface — `type=*TxManager,
// value=nil`). The bare `tr == nil` check returns false on a typed-nil, so
// without IsNilInterface the wrapper would emit a non-nil sealed value
// hiding the inner nil — caller-side `if tr == nil` checks would be
// silently bypassed and the first RunInTx would panic.
//
// Repro of pre-fix bug: replacing validation.IsNilInterface with `tr == nil`
// makes this test fail (WrapForCell(typedNil) returns a non-nil sealed
// wrapper around the nil pointer).
func TestCellTxManager_WrapTypedNilReturnsNil(t *testing.T) {
	t.Parallel()
	var p *fakeTxRunner
	var tr persistence.TxRunner = p
	if persistence.WrapForCell(tr) != nil {
		t.Fatal("WrapForCell(typed-nil) must return nil interface, not a sealed wrapper hiding nil pointer")
	}
}

// TestCellTxManager_WrapDelegatesRunInTx pins the transparent-proxy
// invariant: cell.go fields hold CellTxManager, but service constructors
// still expect raw TxRunner — the wrapper must satisfy TxRunner and
// forward RunInTx without altering semantics.
func TestCellTxManager_WrapDelegatesRunInTx(t *testing.T) {
	t.Parallel()
	f := &fakeTxRunner{}
	wrapped := persistence.WrapForCell(f)
	if wrapped == nil {
		t.Fatal("WrapForCell(non-nil) must not return nil")
	}
	var ran bool
	err := wrapped.RunInTx(context.Background(), func(_ context.Context) error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("RunInTx returned err: %v", err)
	}
	if !ran || f.runs != 1 {
		t.Fatalf("delegate failed: ran=%v runs=%d", ran, f.runs)
	}
}

// TestCellTxManager_SatisfiesTxRunner is a compile-time assertion that
// CellTxManager embeds TxRunner. Cells store fields typed as CellTxManager
// and pass them directly to service.NewXxx (which accepts TxRunner) without
// re-wrapping.
func TestCellTxManager_SatisfiesTxRunner(t *testing.T) {
	t.Parallel()
	var asTxRunner persistence.TxRunner = persistence.WrapForCell(&fakeTxRunner{})
	if asTxRunner == nil {
		t.Fatal("CellTxManager must satisfy persistence.TxRunner")
	}
}

// nooperTxRunner is a Nooper-implementing TxRunner used to verify the
// internalCellTxManager.Noop() pass-through preserves cell.CheckNotNoop's
// durable-rejection signal (mirrors kernel/cell.DemoTxRunner shape; we
// redefine it locally to keep this package free of kernel/cell imports —
// kernel/persistence does not depend on kernel/cell).
type nooperTxRunner struct{}

func (nooperTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

func (nooperTxRunner) Noop() bool { return true }

// TestWrapForCell_PreservesNooperPassThrough is the end-to-end regression
// for the Noop pass-through: WrapForCell(nooperTxRunner{}).(Nooper).Noop()
// must return true. Without internalCellTxManager.Noop() the embed-of-
// interface field would hide the inner Nooper method, breaking
// cell.CheckNotNoop's durable mode rejection of demo runners.
//
// kernel/cell.CheckNotNoop is exercised end-to-end in
// cells/<x>/cell_test.go::TestCheckNotNoop_DurableMode (path through
// cell.ResolveCellEmitter). This unit-level test pins the contract at the
// kernel/persistence boundary so a refactor that removes the Noop
// pass-through fails here, before reaching cell-level integration tests.
//
// Repro of pre-fix bug: removing internalCellTxManager.Noop() makes this
// test fail (type assertion `_, ok := wrapped.(nooper); ok` returns false).
func TestWrapForCell_PreservesNooperPassThrough(t *testing.T) {
	t.Parallel()
	wrapped := persistence.WrapForCell(nooperTxRunner{})
	type nooper interface{ Noop() bool }
	n, ok := wrapped.(nooper)
	if !ok {
		t.Fatal("CellTxManager wrap must expose inner Nooper interface")
	}
	if !n.Noop() {
		t.Fatal("wrapped nooperTxRunner.Noop() must return true (passthrough)")
	}
}

// TestWrapForCell_NonNooperReturnsFalse confirms the pass-through default:
// when the inner TxRunner does NOT implement Nooper, the wrapper's Noop()
// returns false (durable mode accepts the runner as a real implementation).
func TestWrapForCell_NonNooperReturnsFalse(t *testing.T) {
	t.Parallel()
	wrapped := persistence.WrapForCell(&fakeTxRunner{})
	type nooper interface{ Noop() bool }
	n, ok := wrapped.(nooper)
	if !ok {
		t.Fatal("CellTxManager always implements Noop() by structure")
	}
	if n.Noop() {
		t.Fatal("non-Nooper inner TxRunner must produce Noop()==false")
	}
}
