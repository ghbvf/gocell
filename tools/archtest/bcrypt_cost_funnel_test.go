// INVARIANT: BCRYPT-COST-FUNNEL-01
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestBCRYPT_COST_FUNNEL_01 dogfoods CheckBcryptCostFunnel01 — the single rule
// body covering both sub-rules — so the exact scan an external cell would import
// is the one GoCell enforces (no parallel inline rule body):
//   - A1 "single sanctioned holder": bcrypt.GenerateFromPassword may appear only
//     in credential/hasher.go (replaces the compile-time guarantee the deleted
//     domain.BcryptCost const gave); test files may hash fixtures directly, which
//     is out of A1's production-only scope.
//   - A2 "typed function choice": credential.NewTestHasher (the only cost-bearing
//     door) may be called only from *_test.go or sanctioned test-support packages;
//     production wires NewProductionHasher (no cost knob).
func TestBCRYPT_COST_FUNNEL_01(t *testing.T) {
	t.Parallel()
	Report(t, "BCRYPT-COST-FUNNEL-01", CheckBcryptCostFunnel01(t, ConfigForExternalCell{}))
}

// TestBCRYPT_COST_FUNNEL_01_A1_RedFixture asserts the A1 detector fires on a
// known-positive: internal/bcryptcostredfixture/fixture.go deliberately calls
// bcrypt.GenerateFromPassword. The fixture lives under tools/archtest/internal,
// which the scanner excludes from the production scan, so it does not pollute
// A1's real findings. Without this, a broken detector would silently pass.
func TestBCRYPT_COST_FUNNEL_01_A1_RedFixture(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	fixturePath := filepath.Join(root, "tools", "archtest", "internal", "bcryptcostredfixture", "fixture.go")
	line, ok, err := firstQualifiedSelectorLine(fixturePath, bcryptModulePath, "bcrypt", "GenerateFromPassword")
	require.NoError(t, err, "parse A1 RED fixture")
	if !ok {
		t.Error("BCRYPT-COST-FUNNEL-01 A1 RED fixture: detector found no bcrypt.GenerateFromPassword in fixture.go; " +
			"firstQualifiedSelectorLine may be broken")
		return
	}
	t.Logf("A1 RED fixture hit at fixture.go:%d", line)
}

// TestBCRYPT_COST_FUNNEL_01_A2_RedFixture asserts the A2 detector fires on a
// known-positive: credential.NewTestHasher called from a (would-be) non-test
// file. Unlike A1's committed fixture (which imports the public bcrypt package
// and compiles), an A2 fixture would have to import cells/accesscore/internal/credential
// — illegal from tools/archtest under Go's internal rule, and committing a
// //go:build ignore file introduces an "ignore" build tag that the repo's
// build-tag governance rejects. So the source is written to a temp file and
// parsed (never compiled): parser.ParseFile reads it, the toolchain never does.
func TestBCRYPT_COST_FUNNEL_01_A2_RedFixture(t *testing.T) {
	t.Parallel()
	src := "package redfixture\n\n" +
		"import \"" + credentialModulePath + "\"\n\n" +
		"var _ = credential.NewTestHasher(4)\n"
	path := filepath.Join(t.TempDir(), "a2_red.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	line, ok, err := firstQualifiedSelectorLine(path, credentialModulePath, "credential", "NewTestHasher")
	require.NoError(t, err, "parse A2 RED fixture")
	if !ok {
		t.Error("BCRYPT-COST-FUNNEL-01 A2 RED fixture: detector found no credential.NewTestHasher; " +
			"firstQualifiedSelectorLine may be broken")
		return
	}
	t.Logf("A2 RED fixture hit at line %d", line)
}

// TestBCRYPT_COST_FUNNEL_01_NoDotImportBlindSpot closes the dot-import blind
// spot for BOTH funnel symbols: a dot-import would make GenerateFromPassword /
// NewTestHasher a bare ident the SelectorExpr scan misses. Asserts no file
// dot-imports bcrypt or the credential package (also globally banned by revive).
func TestBCRYPT_COST_FUNNEL_01_NoDotImportBlindSpot(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := scanner.ModuleScope(root, scanner.IncludeTests()).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := relSlash(root, path)
		for _, mod := range []string{bcryptModulePath, credentialModulePath} {
			dot, perr := fileDotImportsModule(path, mod)
			require.NoError(t, perr)
			if dot {
				findings = append(findings, fmt.Sprintf("%s: dot-import of %s evades BCRYPT-COST-FUNNEL-01", rel, mod))
			}
		}
	}
	assert.Empty(t, findings,
		"dot-importing bcrypt or the credential package would make the funnel symbols bare idents and evade the scan; forbidden")
}
