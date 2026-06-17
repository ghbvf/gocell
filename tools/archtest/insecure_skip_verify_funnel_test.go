//go:build archtest

// INVARIANT: INSECURE-SKIP-VERIFY-LITERAL-01
//
// This _test.go only dogfoods + precision-gates the rule. The importable rule
// body (CheckInsecureSkipVerifyLiteral + findInsecureSkipVerifyLiteral +
// isInsecureSkipVerifyAllowedRel + the full package godoc) lives in the
// non-test companion insecure_skip_verify_funnel.go — single source, no
// parallel rule body.
package archtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInsecureSkipVerifyLiteral dogfoods INSECURE-SKIP-VERIFY-LITERAL-01
// against GoCell itself. The sole current literal (tlsutil/client.go) is
// allowlisted; the scan must return ZERO violations.
func TestInsecureSkipVerifyLiteral(t *testing.T) {
	Report(t, insecureSkipVerifyFunnelRuleID,
		CheckInsecureSkipVerifyLiteral(t, ConfigForExternalCell{}))
}

// TestInsecureSkipVerifyLiteral_RedFixtureDetected asserts the production
// findInsecureSkipVerifyLiteral helper flags the violating fixture file
// (anti-vacuity — proves the scan actually fires).
//
// It drives findInsecureSkipVerifyLiteral directly against the fixture
// source file (without the isInsecureSkipVerifyAllowedRel filter) to isolate
// the detection logic from the allowlist — that way a broken allowlist that
// inadvertently skips everything is caught by the dogfood test, not by this
// fixture test.
func TestInsecureSkipVerifyLiteral_RedFixtureDetected(t *testing.T) {
	root := findModuleRoot(t)
	fixturePath := filepath.Join(root, "tools", "archtest", "internal",
		"insecureskipverifyfixture", "redfixture.go")

	// The fixture file is gated by archtest_fixture build tag, so os.ReadFile
	// still works — we parse it ourselves (findInsecureSkipVerifyLiteral reads
	// the raw bytes with parser.ParseFile ignoring build constraints).
	_, err := os.Stat(fixturePath)
	require.NoError(t, err, "fixture file must exist at %s", fixturePath)

	hits, err := findInsecureSkipVerifyLiteral(fixturePath)
	require.NoError(t, err)

	// Equality (not >=) so the fixture cannot drift silently.
	assert.Len(t, hits, 1,
		"fixture must yield exactly 1 INSECURE-SKIP-VERIFY-LITERAL-01 hit "+
			"(the violateInsecureSkipVerifyLiteral function); "+
			"if the fixture changes intentionally, update the expected count")
}

// TestIsInsecureSkipVerifyAllowedRel locks the allowlist predicate that
// exempts framework/runtime/http/tlsutil/ files.
func TestIsInsecureSkipVerifyAllowedRel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rel  string
		want bool
	}{
		{"framework/runtime/http/tlsutil/client.go", true},
		{"framework/runtime/http/tlsutil/server.go", true},
		{"framework/runtime/http/tlsutil/client_test.go", true}, // _test.go already skipped upstream
		{"cellmodules/celltransport/transport.go", false},
		{"adapters/websocket/handler.go", false},
		{"framework/runtime/http/tlsutil2/other.go", false}, // not the same dir
		{"framework/runtime/http/other.go", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.rel, func(t *testing.T) {
			t.Parallel()
			if got := isInsecureSkipVerifyAllowedRel(tc.rel); got != tc.want {
				t.Errorf("isInsecureSkipVerifyAllowedRel(%q) = %v, want %v",
					tc.rel, got, tc.want)
			}
		})
	}
}
