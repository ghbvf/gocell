package persistence

import (
	"context"

	"github.com/ghbvf/gocell/pkg/validation"
)

// CellTxManager is the only TxRunner-shaped type that cells/<x>/cell.go
// public With* Options may accept. The unexported sealedCellTxManager()
// method makes CellTxManager unimplementable outside this package —
// kernel/persistence is the sole entry point via WrapForCell, which
// composition roots must call.
//
// AI-robust 评级：
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
	// ApplyTenantScope sets the RLS app.tenant_id GUC on the AMBIENT transaction
	// mid-flight (must be called inside RunInTx). It exists for the one path that
	// cannot know the tenant at tx-start because it must read a non-RLS row inside
	// the tx to learn it — sessionrefresh (#1617 PR-3b): Peek + sessions.tenant_id
	// reads happen inside the cross-store tx (REFRESH-CROSS-STORE-TX-01), then the
	// derived tenant scopes the subsequent users/roles reads via this call. Backends
	// without row-level security (mem / demo TxRunner) return nil — there is no GUC
	// to scope. The real injection lives in adapters/postgres.TxManager.ApplyTenantScope.
	//
	// canonicalTenantID is the canonical lowercase-UUID tenant string (the typed
	// pkg/tenant.TenantID is enforced upstream at the cells/accesscore/internal/scopedtx
	// funnel; this kernel boundary takes a string so kernel/persistence stays free of
	// pkg/tenant's transitive deps — google/uuid — which would otherwise ripple into
	// every satellite module's go.sum). adapters/postgres re-validates it via
	// tenant.ParseTenantID before writing the GUC (defense in depth).
	ApplyTenantScope(ctx context.Context, canonicalTenantID string) error
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
// when present (kernel/outbox.Nooper). Without this method the sealed wrapper
// would hide outbox.DemoTxRunner's noop signal from outbox.CheckNotNoop, letting
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

// ApplyTenantScope forwards to the wrapped TxRunner's ApplyTenantScope when it
// supports row-level security (the PG TxManager); otherwise it is a no-op (mem /
// demo runners have no GUC to scope). The interface is matched structurally —
// kernel/persistence does not import adapters/postgres — mirroring the Noop()
// delegation above. See the CellTxManager.ApplyTenantScope godoc for semantics.
func (i internalCellTxManager) ApplyTenantScope(ctx context.Context, canonicalTenantID string) error {
	type tenantScoper interface {
		ApplyTenantScope(context.Context, string) error
	}
	if sc, ok := i.TxRunner.(tenantScoper); ok {
		return sc.ApplyTenantScope(ctx, canonicalTenantID)
	}
	return nil
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
//   - examples/<demo>/main.go, examples/<demo>/app.go, and examples/<demo>/run.go composition roots
//     (run.go is the hand-written half of the K#10 main+run split)
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
