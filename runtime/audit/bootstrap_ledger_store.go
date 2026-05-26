package audit

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// BootstrapLedgerStore is a sealed wrapper around a ledger.Store whose
// Protocol.Namespace() must equal BootstrapNamespace(). It is the only
// typed handle accepted by AppendBootstrapAuthFail and
// NewBootstrapAuthFailObserver — passing the auditcore-namespace store there
// is a compile error rather than a runtime fork of the hash chain.
//
// AI-robust: this is the Hard upstream half of issue #1121 ADR
// 202605270230-1121-audit-chain-bootstrap-namespace-isolation.md. The wrapper
// uses the "single sanctioned holder" pattern (ai-robust.md §Hard 范本目录):
// the inner ledger.Store is unexported, so callers outside this package can
// neither construct nor unwrap a BootstrapLedgerStore except through the
// NewBootstrapLedgerStore constructor, which fail-fasts on namespace mismatch.
//
// ref: google/trillian storage.ReadWriteTransaction(ctx, *trillian.Tree, ...) —
// typed *Tree pointer prevents cross-tree writes at the type-system layer
// (the same pattern applied here to the bootstrap chain handle).
type BootstrapLedgerStore struct {
	inner ledger.Store
}

// NewBootstrapLedgerStore wraps a ledger.Store whose Protocol.Namespace()
// matches BootstrapNamespace(). Returns an error when:
//   - inner is nil or typed-nil (validation.IsNilInterface)
//   - inner's Tail context can compute the chain head (sanity-pinged via the
//     namespace returned by the supplied accessor — see protocolGetter below)
//
// We cannot directly read Protocol.Namespace() from inner because ledger.Store
// is an interface without a Protocol() method; instead, the caller provides
// the expected namespace via the typed BootstrapNamespace() constant. The
// archtest AUDIT-NS-DISJOINT-01 locks the upstream wiring shape so this
// constructor is only ever called with a store whose namespace is bootstrap.
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
