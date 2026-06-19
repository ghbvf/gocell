//go:build archtest

// INVARIANT: TLS-TEST-MATERIAL-FUNNEL-01
//
// This _test.go only dogfoods + precision-gates the rule. The importable rule
// body (CheckTLSTestMaterialFunnel + scanTLSTestMaterialViolations +
// isTLSTestMaterialSanctioned + the full package godoc) lives in the non-test
// companion tls_test_material_funnel.go so it is a single source — no parallel
// rule body.
package archtest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTLSTestMaterialFunnel dogfoods TLS-TEST-MATERIAL-FUNNEL-01 against GoCell
// itself: every crypto/x509.CreateCertificate caller must live in a sanctioned
// package, so the workspace scan must return ZERO violations.
func TestTLSTestMaterialFunnel(t *testing.T) {
	Report(t, tlsTestMaterialRuleID, CheckTLSTestMaterialFunnel(t))
}

// TestTLSTestMaterialFunnel_RedFixtureDetected asserts the production scanner
// catches every banned call shape (qualified + aliased) in the RED fixture,
// gated by //go:build archtest_fixture. It runs the same
// scanTLSTestMaterialViolations the production Check uses (single source)
// against the synthetic fixture package — anti-vacuity proof the rule is not
// trivially green.
//
// Coverage: 1 qualified hit + 1 aliased hit (via x509alias) = 2 total.
func TestTLSTestMaterialFunnel_RedFixtureDetected(t *testing.T) {
	diags := Run(t, Fixture(
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/tlsmaterialfixture/..."},
	), scanTLSTestMaterialViolations)

	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}
	// Equality (not >=) so the fixture cannot drift silently — any change to the
	// fixture files must update this count.
	assert.Len(t, diags, 2,
		"fixture must yield exactly 2 TLS-TEST-MATERIAL-FUNNEL-01 hits "+
			"(1 qualified + 1 aliased crypto/x509.CreateCertificate); "+
			"if the fixture changes intentionally, update the expected count")
}

// TestIsTLSTestMaterialSanctioned locks the sanctioned-directory predicate: each
// sanctioned prefix is exempt, a sibling directory sharing a name prefix is NOT
// (trailing-slash boundary), and an arbitrary test path is NOT.
func TestIsTLSTestMaterialSanctioned(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rel  string
		want bool
	}{
		// Pass.Rel strips the framework module's "framework/" prefix → "runtime/…".
		{name: "tlsutiltest file", rel: "runtime/http/tlsutil/tlsutiltest/tlsutiltest.go", want: true},
		{name: "certsigning test", rel: "runtime/certsigning/request_test.go", want: true},
		{name: "certlifecycle test", rel: "runtime/certlifecycle/testsupport_test.go", want: true},
		{name: "softca production", rel: "adapters/softca/ca.go", want: true},
		{name: "tlsutil (non-test-helper) is NOT sanctioned", rel: "runtime/http/tlsutil/client_test.go", want: false},
		{name: "name-prefix sibling is NOT sanctioned", rel: "runtime/certsigningfoo/x_test.go", want: false},
		{name: "arbitrary cell test is NOT sanctioned", rel: "cellmodules/celltls/celltls_test.go", want: false},
		{name: "the RED fixture is NOT sanctioned", rel: "tools/archtest/internal/tlsmaterialfixture/redfixture.go", want: false},
		{name: "empty path", rel: "", want: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isTLSTestMaterialSanctioned(tc.rel); got != tc.want {
				t.Errorf("isTLSTestMaterialSanctioned(%q) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}
