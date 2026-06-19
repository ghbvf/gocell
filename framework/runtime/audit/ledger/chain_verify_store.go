package ledger

import "context"

// ChainRef identifies one per-(namespace, tenant) audit chain plus its observed
// seq_no bounds, as enumerated from an admin read pool in a single grouped scan
// (#1755). It is a CHAIN-IDENTITY value only — it carries no audit row content
// (no payload / actor / hash), so surfacing it to an operator never leaks
// cross-tenant audit data.
//
//   - Namespace is the chain's namespace column (the relay "auditcore" or the
//     "bootstrap" chain). It is a bounded closed set and MAY appear in a report;
//     it is NOT used as a metric label (see ChainVerifier).
//   - TenantID is the chain's tenant_id ("" = the system/framework sub-chain).
//     It is UNBOUNDED and MUST NEVER be used as a metric label (observability.md).
//   - MinSeq / MaxSeq are MIN(seq_no) / MAX(seq_no) for the chain. A healthy
//     chain has MinSeq == 1 (genesis present); MinSeq > 1 means the genesis rows
//     are missing and the orchestrator flags it distinctly rather than treating
//     it as tamper. MaxSeq is the tail — verify [1, MaxSeq].
type ChainRef struct {
	Namespace string
	TenantID  string
	MinSeq    int64
	MaxSeq    int64
}

// ChainVerifyStore is the admin-pool-backed full-chain integrity-verify
// capability behind the #1755 admin audit chain verify tool. It enumerates every
// per-(namespace, tenant) chain and re-computes each chain's HMAC linkage.
//
// # Why it is SEPARATE from CrossTenantQueryStore
//
// CrossTenantQueryStore is the sealed super-admin cross-tenant DATA-read funnel:
// every method takes a tenant.CrossTenantVisibility because it returns audit row
// CONTENT, and that content exposure must route through the mandatory FR-007
// audit. ChainVerifyStore returns only integrity VERDICTS (valid + first-invalid
// seq) — never row content — so it is a system-integrity operation (like
// audit.VerifyBootstrapTailOnStartup, which also takes no obligation), gated by
// possession of the admin pool, not by a CrossTenantVisibility grant. Threading
// the sealed obligation through it would either force a bogus parameter onto an
// integrity op or pollute the data-read funnel with a no-obligation method.
//
// The capability is OPTIONAL: when the admin read pool is not provisioned, the
// orchestrator reports the capability as gracefully absent (mirrors #1810's 501
// fail-closed for cross-tenant reads), never fail-open.
type ChainVerifyStore interface {
	// EnumerateChains returns one ChainRef per (namespace, tenant_id) chain via a
	// single grouped query on the admin pool. The result is the full per-tenant
	// fleet across BOTH namespace chains. Returns an empty (non-nil) slice when no
	// rows exist.
	EnumerateChains(ctx context.Context) ([]ChainRef, error)

	// VerifyChain re-computes the HMAC chain for [fromSeq, toSeq] of the EXPLICIT
	// (namespace, tenant) chain, using the namespace-matched HMAC protocol. It
	// returns valid=true, firstInvalidSeq=-1 when the range is intact, and
	// valid=false with the first invalid seq_no when linkage / recompute fails.
	//
	// A namespace with no registered protocol FAILS CLOSED (a non-nil error, NOT a
	// false valid=false): a missing protocol is a misconfiguration, not tamper, and
	// verifying with the wrong namespace's HMAC key would make every entry look
	// tampered (a false alarm).
	VerifyChain(ctx context.Context, namespace, tenantID string, fromSeq, toSeq int64) (valid bool, firstInvalidSeq int64, err error)
}
