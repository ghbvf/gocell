package persistence

// CellTxManager is the only TxRunner-shaped type that cells/<x>/cell.go
// public With* Options may accept. The unexported sealedCellTxManager()
// method makes CellTxManager unimplementable outside this package —
// kernel/persistence is the sole entry point via WrapForCell, which
// composition roots must call.
//
// AI-HARD per ai-collab.md §"违反不可表达": writing
// `WithFoo(tx persistence.TxRunner) Option` in any cell.go and trying to
// wire it from a composition root will fail at compile time — the type
// system rejects assignment of a raw TxRunner to CellTxManager (the
// composition root must call WrapForCell first), and external packages
// cannot implement CellTxManager (sealedCellTxManager is unexported).
//
// Replaces the predecessor archtest CELL-RAW-DEPS-01 scanner-based Medium
// guard with type-system Hard enforcement.
//
// CellTxManager embeds TxRunner so cells can pass the wrapped value
// directly to internal service constructors — service.NewXxx accepts
// TxRunner and the embedded interface satisfies it transparently, so
// service signatures do not change.
//
// ref: docs/architecture/<adr-cell-raw-infra-sealed-marker>.md §D1
type CellTxManager interface {
	TxRunner
	sealedCellTxManager()
}

// internalCellTxManager is the only implementation of CellTxManager.
// It embeds the raw TxRunner to satisfy RunInTx, plus the sealed marker
// method that gates external implementations.
type internalCellTxManager struct {
	TxRunner
}

func (internalCellTxManager) sealedCellTxManager() {}

// Noop transparently delegates to the wrapped TxRunner's Nooper interface
// when present (kernel/cell.Nooper). Without this method the sealed wrapper
// would hide cell.DemoTxRunner's noop signal from cell.CheckNotNoop, letting
// durable assemblies silently accept demo runners.
//
// The interface is matched structurally — kernel/persistence does not import
// kernel/cell, so we redeclare the single-method shape locally.
func (i internalCellTxManager) Noop() bool {
	type nooper interface{ Noop() bool }
	if n, ok := i.TxRunner.(nooper); ok {
		return n.Noop()
	}
	return false
}

// WrapForCell is the sole authorized path for handing a TxRunner to a
// cell's With* Option. Returns nil when tr is nil so caller-side
// typed-nil detection (e.g. validation.IsNilInterface in builder options)
// keeps working.
//
// Allowed callers (enforced by archtest CELL-RAW-INFRA-WRAPPER-LOCATION-01):
//   - cmd/* composition roots
//   - examples/<demo>/main.go and examples/<demo>/app.go composition roots
//   - *_test.go in any layer
//   - kernel/persistence/cell_marker.go (this file)
//
// Adding a new caller requires updating both the archtest allowlist and
// reviewing whether the new path is truly composition-root.
func WrapForCell(tr TxRunner) CellTxManager {
	if tr == nil {
		return nil
	}
	return internalCellTxManager{TxRunner: tr}
}
