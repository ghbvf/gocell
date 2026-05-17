//go:build archtest_fixture

// Package auditledgerfixture is a deliberate
// AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01 negative fixture loaded only when
// the archtest_fixture build tag is set.
//
// The fixture imports `runtime/audit/ledger` under a non-default alias
// (`auditledger`) and calls `auditledger.MustNewProtocol(nil)`. The legacy
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
// forbidden ledger constructor (NewProtocol / MustNewProtocol). The companion
// test asserts hits == 1; adding a second call site or removing the call
// breaks the contract.
package auditledgerfixture

import auditledger "github.com/ghbvf/gocell/runtime/audit/ledger"

// AliasedMustNewProtocol intentionally invokes MustNewProtocol through a
// non-default import alias. The return value is discarded; the function is
// never called at runtime — the fixture exists for AST/type-info analysis.
//
// MustNewProtocol's signature is `func MustNewProtocol(opts ...Option) *Protocol`
// (variadic), so the zero-arg form below compiles and is a real call expression
// that ResolvePackageRef sees as `auditledger.MustNewProtocol`. A future
// signature change to required parameters would cause a clean build error
// in this fixture, not silent drift.
func AliasedMustNewProtocol() {
	_ = auditledger.MustNewProtocol()
}
