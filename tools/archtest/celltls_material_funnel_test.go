//go:build archtest

// INVARIANT: CELLTLS-MATERIAL-FUNNEL-01
//
// This _test.go only dogfoods + precision-gates the rule. The importable rule
// body (CheckCellTLSMaterialFunnel + scanCellTLSMaterialViolations + the full
// package godoc, all module-path-agnostic via PlatformFrameworkModulePath)
// lives in the non-test companion celltls_material_funnel.go so it is
// fork-safe — single source, no parallel rule body.
package archtest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCellTLSMaterialFunnel dogfoods CELLTLS-MATERIAL-FUNNEL-01 against GoCell
// itself. celltls.Resolve is the only caller of tlsutil.NewClientIdentity and
// tlsutil.NewServerMTLSConfig; it is the sanctioned site (cellmodules/celltls)
// and is allowlisted — the scan must return ZERO violations.
func TestCellTLSMaterialFunnel(t *testing.T) {
	Report(t, cellTLSFunnelRuleID, CheckCellTLSMaterialFunnel(t, ConfigForExternalCell{}))
}

// TestCellTLSMaterialFunnel_RedFixtureDetected asserts the production rule
// catches every banned call shape (qualified / aliased) in the RED fixture,
// gated by `//go:build archtest_fixture`. It runs the same
// scanCellTLSMaterialViolations the production Check uses (single source)
// against the synthetic fixture package.
//
// Coverage: 2 qualified hits (NewClientIdentity + NewServerMTLSConfig) +
// 1 aliased hit (NewClientIdentity via tlsalias) = 3 total.
func TestCellTLSMaterialFunnel_RedFixtureDetected(t *testing.T) {
	diags := Run(t, Fixture(
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/celltlsfixture/..."},
	), scanCellTLSMaterialViolations)

	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}
	// Equality (not >=) so the fixture cannot drift silently — any change to
	// the fixture files must update this count.
	assert.Len(t, diags, 3,
		"fixture must yield exactly 3 CELLTLS-MATERIAL-FUNNEL-01 hits "+
			"(1 qualified NewClientIdentity + 1 qualified NewServerMTLSConfig + "+
			"1 aliased NewClientIdentity); "+
			"if the fixture changes intentionally, update the expected count")
}

// TestIsCellTLSSanctionedCall locks the (pkg, ctor) granularity (#2263 F4): the
// exemption applies per exact package path AND per constructor. cellmodules/celltls
// may call both ctors; adapters/grpc only NewServerMTLSConfig — crucially NOT
// NewClientIdentity (a cross-cell client identity is celltls-only). A consumer
// module forging the same relative package name is NOT exempt.
func TestIsCellTLSSanctionedCall(t *testing.T) {
	t.Parallel()
	celltlsPkg := PlatformModulePath + "/cellmodules/celltls"
	grpcPkg := PlatformModulePath + "/adapters/grpc"
	cases := []struct {
		name    string
		pkgPath string
		ctor    string
		want    bool
	}{
		{name: "celltls + NewClientIdentity", pkgPath: celltlsPkg, ctor: "NewClientIdentity", want: true},
		{name: "celltls + NewServerMTLSConfig", pkgPath: celltlsPkg, ctor: "NewServerMTLSConfig", want: true},
		{name: "grpc + NewServerMTLSConfig", pkgPath: grpcPkg, ctor: "NewServerMTLSConfig", want: true},
		{name: "grpc + NewClientIdentity → NOT sanctioned (F4)", pkgPath: grpcPkg, ctor: "NewClientIdentity", want: false},
		{name: "consumer forges celltls path", pkgPath: "consumer.example/cellmodules/celltls", ctor: "NewClientIdentity", want: false},
		{name: "consumer forges grpc path", pkgPath: "consumer.example/adapters/grpc", ctor: "NewServerMTLSConfig", want: false},
		{
			name: "platform non-sanctioned package", pkgPath: PlatformModulePath + "/cellmodules/celltransport",
			ctor: "NewClientIdentity", want: false,
		},
		{name: "unresolved package", pkgPath: "", ctor: "NewClientIdentity", want: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isCellTLSSanctionedCall(tc.pkgPath, tc.ctor); got != tc.want {
				t.Errorf("isCellTLSSanctionedCall(%q, %q) = %v, want %v",
					tc.pkgPath, tc.ctor, got, tc.want)
			}
		})
	}
}
