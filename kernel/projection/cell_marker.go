package projection

import "github.com/ghbvf/gocell/pkg/validation"

// CellCheckpointStore is the only CheckpointStore-shaped type that a cell's
// public With* Options may accept. The unexported sealedCellCheckpointStore()
// method makes it unimplementable outside kernel/projection — this package is
// the sole entry point via WrapCheckpointStoreForCell, which composition roots
// (cmd/*) call. This mirrors outbox.CellPublisher / persistence.CellTxManager.
//
// AI-robust 评级：
//   - 字段/赋值层：Hard（sealed marker，外部不可表达 internalCellCheckpointStore 字面量）
//   - 公开 With* Option 签名层：Medium（CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01 在
//     PR-04 cell 接入投影时按需扩 rawPublicOptionForbidden；PR-00 无 cell option）
//
// CellCheckpointStore embeds CheckpointStore so cells can pass the wrapped value
// directly wherever a CheckpointStore is expected — the embedded interface
// satisfies those sites transparently, so internal harness API does not change.
//
// ref: docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md §D1
// ref: kernel/outbox/cell_marker.go (CellPublisher precedent).
type CellCheckpointStore interface {
	CheckpointStore
	// MARKER: do not implement; this is the sealing marker — call
	// projection.WrapCheckpointStoreForCell(...) from your composition root instead.
	sealedCellCheckpointStore()
}

// internalCellCheckpointStore is the only implementation of CellCheckpointStore.
type internalCellCheckpointStore struct {
	CheckpointStore
}

func (internalCellCheckpointStore) sealedCellCheckpointStore() {}

// Noop transparently delegates to the wrapped CheckpointStore's Nooper interface
// when present. Required by SEALED-MARKER-NOOP-TRANSPARENCY-01 (every kernel
// internalCell* sealed marker must forward Noop) so a demo/in-memory checkpoint
// store's noop signal is not hidden from durable-mode rejection. The interface
// is matched structurally — kernel/projection does not import kernel/cell.
func (i internalCellCheckpointStore) Noop() bool {
	type nooper interface{ Noop() bool }
	if n, ok := i.CheckpointStore.(nooper); ok {
		return n.Noop()
	}
	return false
}

// WrapCheckpointStoreForCell is the sole authorized path for handing a
// CheckpointStore to a cell's With* Option. Returns nil when s is bare-nil OR a
// typed-nil interface (e.g. `var s *postgres.ProjectionCheckpointStore`) so
// caller-side typed-nil detection in builder options keeps working — without
// IsNilInterface the wrapper would emit a non-nil sealed value hiding the inner
// nil, silently bypassing Init() fail-fast guards. Mirror of
// outbox.WrapPublisherForCell.
//
// Allowed callers (enforced by archtest CELL-RAW-INFRA-WRAPPER-LOCATION-01 once
// PR-02/PR-04 wire real callers):
//   - cmd/* composition roots
//   - examples/<demo>/main.go, examples/<demo>/app.go, examples/<demo>/run.go
//   - *_test.go in any layer
//   - kernel/projection/cell_marker.go (this file)
func WrapCheckpointStoreForCell(s CheckpointStore) CellCheckpointStore {
	if validation.IsNilInterface(s) {
		return nil
	}
	return internalCellCheckpointStore{CheckpointStore: s}
}
