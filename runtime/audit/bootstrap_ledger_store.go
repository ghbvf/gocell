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
// Construct with NewBootstrapLedgerStore(inner) at the composition root
// (cmd/corebundle / examples/*/app.go) after building the
// BootstrapNamespace()-scoped ledger.Store, and hand the returned pointer
// to NewBootstrapAuthFailObserver. The Append/Tail/Verify/RepoReady methods
// delegate to the wrapped store; the `inner` field is unexported and no
// accessor returns it, so callers cannot extract the wrapped store and
// route writes around the sealed surface.
//
// ref: google/trillian storage.ReadWriteTransaction(ctx, *trillian.Tree, ...) —
// typed *Tree pointer prevents cross-tree writes at the type-system layer
// (the same downstream pattern applied here to the bootstrap chain handle).
//
// Enforcement note (AI-robust grading, see ADR 202605270230 §AI-robust):
// downstream is Hard (the type-checker rejects every bare `ledger.Store`
// argument at the AppendBootstrapAuthFail / NewBootstrapAuthFailObserver call
// sites); upstream is also Hard because ledger.Store exposes Protocol() and
// NewBootstrapLedgerStore rejects any store not scoped to BootstrapNamespace().
// AUDIT-NS-DISJOINT-01 remains a composition-root backstop for production
// wiring, but namespace correctness no longer depends on that static scan.
type BootstrapLedgerStore struct {
	inner ledger.Store
}

// NewBootstrapLedgerStore wraps an inner ledger.Store as the bootstrap-chain
// handle. Returns an error when:
//   - inner is nil or typed-nil (validation.IsNilInterface)
//   - inner.Protocol() is nil
//   - inner.Protocol().Namespace() is not BootstrapNamespace()
func NewBootstrapLedgerStore(inner ledger.Store) (*BootstrapLedgerStore, error) {
	if validation.IsNilInterface(inner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: NewBootstrapLedgerStore requires non-nil ledger.Store")
	}
	protocol := inner.Protocol()
	if protocol == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: NewBootstrapLedgerStore requires ledger.Store with non-nil Protocol")
	}
	if actual := protocol.Namespace(); actual != BootstrapNamespace() {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: NewBootstrapLedgerStore requires bootstrap namespace",
			errcode.WithDetails(
				errcode.PublicString("expectedNamespace", string(BootstrapNamespace())),
				errcode.PublicString("actualNamespace", string(actual)),
			))
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
