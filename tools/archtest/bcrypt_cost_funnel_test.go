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

// TestBCRYPT_COST_FUNNEL_01_A1_SingleHashOrigin fails if bcrypt.GenerateFromPassword
// appears in any production (non-test) file other than credential/hasher.go.
// Replaces the compile-time guarantee the deleted domain.BcryptCost const gave.
func TestBCRYPT_COST_FUNNEL_01_A1_SingleHashOrigin(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	// ModuleScope without IncludeTests → production files only. Test files may
	// hash fixtures directly (seed a known password into a store); that is not
	// production hashing and is out of A1's scope.
	files, err := scanner.ModuleScope(root).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := relSlash(root, path)
		if rel == bcryptHasherRel {
			continue // the sanctioned holder
		}
		line, ok, perr := firstQualifiedSelectorLine(path, bcryptModulePath, "bcrypt", "GenerateFromPassword")
		require.NoError(t, perr)
		if ok {
			findings = append(findings, fmt.Sprintf(
				"%s:%d: bcrypt.GenerateFromPassword outside credential.Hasher", rel, line,
			))
		}
	}
	assert.Empty(t, findings,
		"all password hashing must route through cells/accesscore/internal/credential.Hasher "+
			"(NewProductionHasher / NewTestHasher); bcrypt.GenerateFromPassword may appear only in "+bcryptHasherRel)
}

// TestBCRYPT_COST_FUNNEL_01_A2_TestHasherCallerAllowlist fails if
// credential.NewTestHasher is called from any non-test, non-allowlisted file.
// The production path has only NewProductionHasher (no cost knob), so there is
// no way to express a weaker-cost production hasher.
func TestBCRYPT_COST_FUNNEL_01_A2_TestHasherCallerAllowlist(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := scanner.ModuleScope(root, scanner.IncludeTests()).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := relSlash(root, path)
		if newTestHasherCallerAllowed(rel) {
			continue
		}
		line, ok, perr := firstQualifiedSelectorLine(path, credentialModulePath, "credential", "NewTestHasher")
		require.NoError(t, perr)
		if ok {
			findings = append(findings, fmt.Sprintf(
				"%s:%d: credential.NewTestHasher called outside test code", rel, line,
			))
		}
	}
	assert.Empty(t, findings,
		"credential.NewTestHasher is the low-cost test door; it may be called only from *_test.go "+
			"or sanctioned test-support packages. Production wires credential.NewProductionHasher().")
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
