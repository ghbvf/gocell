// invariants:
//   - INVARIANT: SESSION-PROTOCOL-COMPOSITION-ROOT-01 (RED fixture coverage)
package archtest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSessionProtocol_RedFixtureDetected asserts the production rule (driven
// by scanSessionProtocolViolations) catches every banned call shape in the
// RED fixture at tools/archtest/internal/sessionprotocolfixture/ (gated by
// `//go:build archtest_fixture`).
//
// # Why a RED fixture is required
//
// Without a fixture, the production rule's zero-diagnostic outcome on
// real cells/runtime/adapters carries no information: it could mean the
// rule works AND no violation exists, OR the rule is broken and silently
// passes everything. The fixture provides a known-positive sample so an
// accidental rule regression turns this test red.
//
// # Coverage matrix
//
// The fixture contains 3 banned call sites: 3 callee shapes × 1 banned
// function name (NewProtocol; MustNewProtocol was deleted by B2-K-02).
// Each shape exercises a distinct branch of archtest.ResolvePackageRef:
//
//	┌────────────────┬─────────────────────────────────────┬──────────────────────────────┐
//	│ Callee shape   │ AST form                            │ ResolvePackageRef branch     │
//	├────────────────┼─────────────────────────────────────┼──────────────────────────────┤
//	│ qualified      │ session.NewProtocol(...)            │ Uses[sel.X].(*types.PkgName) │
//	│ aliased import │ sess.NewProtocol(...)               │ Uses[sel.X].(*types.PkgName) │
//	│ dot import     │ NewProtocol(...) after `. "...sess"`│ Uses[id].(*types.Func)       │
//	└────────────────┴─────────────────────────────────────┴──────────────────────────────┘
//
// All three branches resolve to the same (pkgPath, name) tuple
// ("github.com/ghbvf/gocell/runtime/auth/session", "NewProtocol") — the
// type-aware rule does not care about the source callee shape.
func TestSessionProtocol_RedFixtureDetected(t *testing.T) {
	diags := RunTypedFixture(t,
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/sessionprotocolfixture/..."},
		scanSessionProtocolViolations,
	)

	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}

	// 3 callee shapes × 1 banned function name = 3 expected hits. We use
	// equality (not ≥ 3) so the fixture cannot drift into producing extra
	// or fewer violations silently — any change to redfixture.go /
	// dotimport.go must also update the expected count here.
	assert.Len(t, diags, 3,
		"fixture must yield exactly 3 SESSION-PROTOCOL-COMPOSITION-ROOT-01 hits "+
			"(qualified + aliased + dot × NewProtocol); "+
			"if the fixture changes intentionally, update the expected count")
}
