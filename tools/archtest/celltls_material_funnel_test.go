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

// TestIsCellTLSSanctionedSite locks the platform-identity bind on the
// sanctioned-site allowlist: the exemption only applies to the exact two
// sanctioned package paths (cellmodules/celltls and adapters/grpc). A consumer
// module that wires this importable rule via cfg.ExtraRules and forges the
// same relative package name must NOT be exempt.
func TestIsCellTLSSanctionedSite(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		pkgPath string
		want    bool
	}{
		{
			name:    "sanctioned platform celltls package",
			pkgPath: PlatformModulePath + "/cellmodules/celltls",
			want:    true,
		},
		{
			name:    "sanctioned platform grpc adapter",
			pkgPath: PlatformModulePath + "/adapters/grpc",
			want:    true,
		},
		{
			name:    "consumer module forges celltls path",
			pkgPath: "consumer.example/cellmodules/celltls",
			want:    false,
		},
		{
			name:    "consumer module forges grpc adapter path",
			pkgPath: "consumer.example/adapters/grpc",
			want:    false,
		},
		{
			name:    "platform package, non-sanctioned path",
			pkgPath: PlatformModulePath + "/cellmodules/celltransport",
			want:    false,
		},
		{
			name:    "unresolved package",
			pkgPath: "",
			want:    false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isCellTLSSanctionedSite(tc.pkgPath); got != tc.want {
				t.Errorf("isCellTLSSanctionedSite(%q) = %v, want %v",
					tc.pkgPath, got, tc.want)
			}
		})
	}
}
