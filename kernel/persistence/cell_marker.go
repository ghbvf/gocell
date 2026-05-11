package persistence

import "github.com/ghbvf/gocell/pkg/validation"

// CellTxManager is the only TxRunner-shaped type that cells/<x>/cell.go
// public With* Options may accept. The unexported sealedCellTxManager()
// method makes CellTxManager unimplementable outside this package —
// kernel/persistence is the sole entry point via WrapForCell, which
// composition roots must call.
//
// AI-rebust 评级：
//   - 字段/赋值层：Hard（sealed marker，外部不可表达 internalCellTxManager 字面量）
//   - 公开 With* Option 签名层：Medium（CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01 archtest type-aware 守）
//
// 双重防线，参见 ADR 202605101900-adr-cell-raw-infra-sealed-marker §D2。
//
// CellTxManager embeds TxRunner so cells can pass the wrapped value
// directly to internal service constructors — service.NewXxx accepts
// TxRunner and the embedded interface satisfies it transparently, so
// service signatures do not change.
//
// ref: docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md §D1
type CellTxManager interface {
	TxRunner
	// MARKER: do not implement; this is the sealing marker — call persistence.WrapForCell(...) from your composition root instead.
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
// cell's With* Option. Returns nil when tr is bare-nil OR a typed-nil
// interface (e.g. `var p *postgres.TxManager`) so caller-side typed-nil
// detection in builder options keeps working — without IsNilInterface
// the wrapper would emit a non-nil sealed value hiding the inner nil
// pointer, silently bypassing CheckNotNoop / Init() fail-fast guards.
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
	if validation.IsNilInterface(tr) {
		return nil
	}
	return internalCellTxManager{TxRunner: tr}
}
