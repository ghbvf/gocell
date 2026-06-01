package composition

import (
	"context"

	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/audit"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// ModuleExports is the typed, additive set of values a module hands to later
// modules during [Builder.Build]. Each field is a sealed domain type; a zero
// ModuleExports means "this module exported nothing". Builder threads exports
// left-to-right (see [Builder.Build]): a later module reads what earlier modules
// produced via the in parameter of [CellModule.Provide].
//
// This replaces the former mutable-bag handoff (a module writing a field on the
// shared *SharedDeps mid-Build): the data flow is now an explicit typed return,
// not a hidden field write, and a reader fails fast if its required upstream
// export is absent.
type ModuleExports struct {
	// BootstrapLedgerStore is the sealed handle to the bootstrap auth-fail audit
	// chain. It is produced by the auditcore module's Provide and consumed by the
	// accesscore module's Provide (audit.NewBootstrapAuthFailObserver). auditcore
	// MUST be registered before accesscore in [Builder.With]
	// (MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01); accesscore fails fast if this
	// is nil.
	BootstrapLedgerStore *audit.BootstrapLedgerStore

	// AuditQueryStore is the read-side ledger.QueryStore that spans all audit
	// chains (auditcore relay + bootstrap). It is produced by the auditcore
	// module's Provide and consumed by the composition root's RuntimeOptionsFunc
	// to construct the correlate reverse-lookup service (#1048 Batch 3). The
	// store is the same multiStore instance passed to auditcell.WithQueryStore,
	// ensuring no second store is constructed.
	//
	// Exactly one producer (auditcore) is expected in the corebundle assembly.
	// merge() applies last-non-nil-wins semantics: if a second module were to
	// set this field, it would silently overwrite the auditcore value. Avoid
	// registering a second producer; control ordering via Builder.With if you
	// intentionally need a replacement store.
	AuditQueryStore ledger.QueryStore
}

// merge returns the combination of in and out: each non-zero field of out
// overrides in, while zero fields of out preserve in. It lets Builder accumulate
// exports across modules without any module clobbering an earlier producer's
// value with a zero.
func (in ModuleExports) merge(out ModuleExports) ModuleExports {
	if out.BootstrapLedgerStore != nil {
		in.BootstrapLedgerStore = out.BootstrapLedgerStore
	}
	if out.AuditQueryStore != nil {
		in.AuditQueryStore = out.AuditQueryStore
	}
	return in
}

// CellModule is the contract by which a Cell declares itself to [Builder.Build].
// Each Cell provides a single *_module.go file that implements this interface,
// self-managing all Cell-specific dependency wiring (KeyProvider, PoolResource,
// cellOpts, etc.).
//
// ref: uber-go/fx fx.Module(name, opts...) — each module is self-contained and
// registers its own providers.
// ref: Go proverbs "accept interfaces, return structs" — when there is only one
// concrete implementation, the interface is introduced at the point where a
// second impl appears; CellModule has multiple impls (one per Cell) so the
// interface is appropriate here.
type CellModule interface {
	// ID returns a stable identifier used in error messages and logs.
	ID() string

	// Provide resolves Cell-specific dependencies from the shared context and
	// the accumulated upstream module exports, and returns:
	//
	//   - cell: the constructed Cell. Must be non-nil on success.
	//   - exports: typed values this module hands to later modules (zero
	//     [ModuleExports] if it exports nothing).
	//   - opts: bootstrap.Options the Cell needs (e.g. WithManagedResource for
	//     its PoolResource).
	//   - provisional: ManagedResources opened during Provide that must be
	//     closed if a subsequent module's Provide fails before bootstrap.Run
	//     activates the lifecycle. The caller ([Builder.Build]) owns rollback:
	//     on any failure it calls Close(ctx) in reverse order on all accumulated
	//     provisional resources.
	//
	// Modules MUST include in provisional every external connection opened
	// during Provide (PG pool, vault client, …) so Build can release them when
	// the assembly cannot complete. The same resources must also appear in the
	// returned opts via bootstrap.WithManagedResource so that bootstrap.Run
	// manages their lifecycle on the happy path.
	//
	// Cross-module values flow through the in parameter (what earlier modules
	// exported) and the returned exports (what this module hands downstream).
	// A module that consumes an upstream export must be registered after its
	// producer in [Builder.With]; consult [ModuleExports] field godoc for the
	// ordering constraints.
	Provide(ctx context.Context, shared *SharedDeps, in ModuleExports) (
		cell.Cell, ModuleExports, []bootstrap.Option, []kernellifecycle.ManagedResource, error)
}
