package metadata

// FrameworkOwnerSentinel is the reserved ownerCell value that marks a contract
// as FRAMEWORK-owned rather than cell-owned. It names the framework/control-plane
// as the contract's definition owner — the SPIFFE/cert-manager/k8s-SIG pattern
// where a neutral, provider-agnostic contract (whose correctness requires
// interchangeable providers) is owned by the framework, never by a single
// consumer cell. The leading underscore mirrors the metrics _runtime sentinel
// and is not a legal cell id (cells are no-dash concat ids), so it can never
// collide with a real ownerCell.
//
// A framework-owned contract is governed by the FRAMEWORK-OWNED-CONTRACT-SCOPED
// rule (not the cell-owner reference rules REF-03/REF-13): it is structurally
// excluded from cell-owner validation by ContractOwner.Cell returning ok=false,
// not by per-rule string escapes.
const FrameworkOwnerSentinel = "_framework"

// ContractOwner is the sealed, resolved owner of a contract: either a platform
// Cell (by id) or the Framework. It is the typed union that replaces ad-hoc
// `ownerCell string` cell-existence assumptions.
//
// Sealed construction (Hard, same shape as runtime/observability/metrics.CellLabel
// and kernel/outbox.Entry): both fields are unexported and the sole constructor
// is [ContractMeta.Owner] (via the package-private resolveContractOwner funnel),
// so a non-zero ContractOwner — and in particular a FRAMEWORK owner — cannot be
// forged outside this package by a struct literal. Code that needs the owning
// cell MUST go through [ContractOwner.Cell]; a framework owner returns ok=false
// there, making "treat the framework as if it were a cell" unexpressible at the
// type level rather than catchable only by a runtime string check.
type ContractOwner struct {
	// cell is the owning cell id when framework is false. Empty cell with
	// framework false is the "owner omitted" error state (flagged by CH-01).
	cell string
	// framework is true when the contract is framework-owned (ownerCell ==
	// FrameworkOwnerSentinel).
	framework bool
}

// resolveContractOwner is the SOLE constructor of a non-zero ContractOwner. It
// is package-private so [ContractMeta.Owner] is the only funnel; the
// FrameworkOwnerSentinel string is recognized here and nowhere else.
func resolveContractOwner(ownerCell string) ContractOwner {
	if ownerCell == FrameworkOwnerSentinel {
		return ContractOwner{framework: true}
	}
	return ContractOwner{cell: ownerCell}
}

// Owner resolves the raw ownerCell wire string (post G-7 auto-derivation) into a
// sealed [ContractOwner]. It is the single funnel through which all
// owner-kind-aware code must read a contract's owner; reading the raw OwnerCell
// field as a cell id is the bypass that CONTRACT-OWNER-CELL-FUNNEL-01 forbids.
func (c *ContractMeta) Owner() ContractOwner {
	return resolveContractOwner(c.OwnerCell)
}

// Cell returns the owning cell id and ok=true when the contract is cell-owned;
// it returns ("", false) when the contract is framework-owned. This is the only
// way to extract a cell from an owner: a framework owner can never be mistaken
// for a cell, because there is no code path that yields a cell id from one.
func (o ContractOwner) Cell() (string, bool) {
	if o.framework {
		return "", false
	}
	return o.cell, true
}

// IsFramework reports whether the contract is framework-owned.
func (o ContractOwner) IsFramework() bool {
	return o.framework
}
