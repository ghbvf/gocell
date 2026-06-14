//go:build archtest_fixture

// Package deviceprincipalfixture is a deliberate
// NO-TEST-DEVICE-PRINCIPAL-IN-PRODUCTION-01 negative fixture loaded only when
// the archtest_fixture build tag is set.
//
// The fixture imports framework/runtime/auth under a non-default alias (`rauth`)
// and calls `rauth.MustNewTestDevicePrincipal(...)`. The legacy AST-only matcher
// (`id.Name == "auth"`) silently passes this shape; the type-aware matcher
// (ResolvePackageRef → *types.PkgName → Imported().Path()) catches it because
// resolution is by canonical import path, not by syntactic identifier.
//
// The build tag excludes this package from `go build ./...` and `go test ./...`
// so it never pollutes real-repo scans. It is loaded explicitly by
// TestNoTestDevicePrincipal_ScannerCatchesAliasBypass via
//
//	archtest.Run(t, archtest.Fixture(archtest.FixtureOpts{Tests: false},
//	    []string{"./tools/archtest/internal/deviceprincipalfixture"}), rule)
//
// AI co-authors who modify the fixture must keep exactly one call to
// MustNewTestDevicePrincipal. The companion test asserts hits == 1; adding a
// second call site or removing the call breaks the anti-vacuity contract.
package deviceprincipalfixture

import rauth "github.com/ghbvf/gocell/framework/runtime/auth"

// AliasedForge intentionally invokes MustNewTestDevicePrincipal through a
// non-default import alias. The return value is discarded; the function is never
// called at runtime — the fixture exists only for AST/type-info analysis.
//
// MustNewTestDevicePrincipal's signature is
// `func MustNewTestDevicePrincipal(subject, tenantID string) *Principal`. The call
// below uses non-empty literal args so the fixture compiles cleanly (it is never
// executed, so the panic-on-empty-args path is irrelevant).
func AliasedForge() {
	_ = rauth.MustNewTestDevicePrincipal("fixture-subject", "00000000-0000-0000-0000-000000000001")
}
