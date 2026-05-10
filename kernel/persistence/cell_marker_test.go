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
