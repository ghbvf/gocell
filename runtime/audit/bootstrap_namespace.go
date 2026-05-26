package audit

import "github.com/ghbvf/gocell/runtime/audit/ledger"

// bootstrapNamespaceLiteral is the single source of truth for the audit chain
// namespace used by bootstrap auth-fail events. It is intentionally distinct
// from the "auditcore" namespace used by the relay-driven appender so that
// the two writers (auditcore relay + bootstrap observer) live on physically
// separate hash chains and cannot fork a shared chain under concurrency.
//
// Validation happens by construction: NamespaceID format rules
// (lowercase / [a-z_] first char / length ≤ 48 / no ':' '{' '}') are checked
// in TestBootstrapNamespace_LiteralPassesValidate so any future drift in the
// validation rules surfaces at unit-test time rather than at first request.
//
// ref: google/trillian storage/log_storage.go — per-tree_id partition with
// independent Merkle roots is the canonical pattern for multi-owner
// tamper-evident logs.
const bootstrapNamespaceLiteral = "bootstrap"

// BootstrapNamespace returns the canonical ledger.NamespaceID used by every
// bootstrap auth-fail audit chain. It is the only sanctioned way to obtain
// this NamespaceID at composition root; archtest AUDIT-NS-DISJOINT-01 locks
// the composition site to call this function rather than passing the literal
// string.
func BootstrapNamespace() ledger.NamespaceID {
	return ledger.NamespaceID(bootstrapNamespaceLiteral)
}
