package audit

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// BootstrapLedgerStore is a sealed handle for the bootstrap audit chain. It
// is the only typed value accepted by AppendBootstrapAuthFail and
// NewBootstrapAuthFailObserver — passing the auditcore-namespace store there
// is a compile error rather than a runtime fork of the hash chain.
//
// Downstream defense (Hard, type system): the type-checker rejects every
// `ledger.Store` argument at the call sites of AppendBootstrapAuthFail and
// NewBootstrapAuthFailObserver — the only way to invoke them is through this
// typed pointer. The `inner` field is unexported and the package exposes no
// accessor that returns it, so callers cannot extract the wrapped store and
// route writes around the sealed surface.
//
// Upstream backstop (Medium, archtest): ledger.Store is an interface without
// a Protocol() method, so `NewBootstrapLedgerStore` cannot itself verify that
// the inner store actually carries `BootstrapNamespace()`. That invariant is
// instead enforced by the AUDIT-NS-DISJOINT-01 archtest, which scans cmd/
// corebundle production wiring for the canonical two-protocol composition.
// The grade is therefore "Hard downstream + Medium upstream" — see ADR
// 202605270230 §AI-robust enforcement. A future Hard upstream upgrade would
// add `Store.Protocol() *Protocol` and an explicit namespace check here.
//
// ref: google/trillian storage.ReadWriteTransaction(ctx, *trillian.Tree, ...) —
// typed *Tree pointer prevents cross-tree writes at the type-system layer
// (the same downstream pattern applied here to the bootstrap chain handle).
type BootstrapLedgerStore struct {
	inner ledger.Store
}

// NewBootstrapLedgerStore wraps an inner ledger.Store as the bootstrap-chain
// handle. Returns an error when:
//   - inner is nil or typed-nil (validation.IsNilInterface)
//
// The inner store's namespace correctness (must equal BootstrapNamespace())
// is enforced by composition-root wiring + the AUDIT-NS-DISJOINT-01 archtest,
// not at construction time — see the type-level godoc above for the grade
// rationale.
func NewBootstrapLedgerStore(inner ledger.Store) (*BootstrapLedgerStore, error) {
	if validation.IsNilInterface(inner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: NewBootstrapLedgerStore requires non-nil ledger.Store")
	}
	return &BootstrapLedgerStore{inner: inner}, nil
}

// Append delegates to the wrapped Store. Callers should normally invoke
// AppendBootstrapAuthFail instead, which constructs the canonical
// bootstrap.auth.fail entry envelope before calling Append.
func (s *BootstrapLedgerStore) Append(ctx context.Context, e *ledger.Entry) error {
	return s.inner.Append(ctx, e)
}

// Tail delegates to the wrapped Store. Exposed for VerifyBootstrapTailOnStartup
// and integration tests; the bootstrap observer path does not call it.
func (s *BootstrapLedgerStore) Tail(ctx context.Context) (ledger.TailSnapshot, error) {
	return s.inner.Tail(ctx)
}

// Verify delegates to the wrapped Store. Used by VerifyBootstrapTailOnStartup
// and integration tests to confirm chain integrity.
func (s *BootstrapLedgerStore) Verify(ctx context.Context, fromSeq, toSeq int64) (bool, int64, error) {
	return s.inner.Verify(ctx, fromSeq, toSeq)
}

// RepoReady delegates to the wrapped Store so the bootstrap chain participates
// in the same differentiated readiness probe surface as the auditcore chain.
// Without this delegate, registering *BootstrapLedgerStore via the cellgen
// RepoProber typed funnel (cells/auditcore/healthz_gen.go) would lose the
// table-level probe — schema/migration drift would only surface at first
// 401/429 instead of at probe-fail-fast time.
//
// Note: in the current corebundle wiring the relay-chain store is the only
// one registered with the audit cell's RepoProber; the bootstrap chain shares
// the same audit_entries table and PG pool, so a separate probe would be
// "禁止同时暴露多个同义 ready probe" per observability.md. Future deployments
// that physically split the chains across pools should register both probes.
func (s *BootstrapLedgerStore) RepoReady(ctx context.Context) error {
	return s.inner.RepoReady(ctx)
}
