//go:build archtest_fixture

// Package auditledgerfixture is a deliberate
// AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01 negative fixture loaded only when
// the archtest_fixture build tag is set.
//
// The fixture imports `runtime/audit/ledger` under a non-default alias
// (`auditledger`) and calls `auditledger.NewProtocol(...)`. The legacy
// AST-only matcher (`pkg.Name == "ledger"`) silently passes this shape; the
// type-aware matcher (typeseval.ResolvePackageRef → *types.PkgName →
// Imported().Path()) catches it because resolution is by canonical import
// path, not by syntactic identifier.
//
// The build tag excludes this package from `go build ./...` and `go test
// ./...` so it never pollutes real-repo scans. It is loaded explicitly by
// TestAuditLedgerProtocol_ScannerCatchesAliasBypass via
//
//	archtest.RunTypedFixture(t, archtest.FixtureOpts{Tests: false},
//	    []string{"./tools/archtest/internal/auditledgerfixture"}, rule)
//
// AI co-authors who modify the fixture must keep exactly one call to a
// forbidden ledger constructor (NewProtocol). The companion test asserts
// hits == 1; adding a second call site or removing the call breaks the
// contract.
//
// History: prior to B2-K-02 this fixture also called ledger.MustNewProtocol
// to exercise the Must variant; MustNewProtocol was deleted, so only
// NewProtocol remains.
package auditledgerfixture

import auditledger "github.com/ghbvf/gocell/runtime/audit/ledger"

// AliasedNewProtocol intentionally invokes NewProtocol through a non-default
// import alias. The return value is discarded; the function is never called
// at runtime — the fixture exists for AST/type-info analysis.
//
// NewProtocol's signature is `func NewProtocol(namespace NamespaceID, key []byte, opts ...Option) (*Protocol, error)`.
// The call below uses the minimum valid arguments (a test namespace and a
// 32-byte zero key) so the fixture compiles cleanly for AST/type-info analysis.
func AliasedNewProtocol() {
	_, _ = auditledger.NewProtocol(auditledger.NamespaceID("fixture"), make([]byte, 32))
}
