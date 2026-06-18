//go:build archtest

// INVARIANT: COMPOSITION-NEWFORROLE-FUNNEL-01
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

// TestCOMPOSITION_NEWFORROLE_FUNNEL_01 dogfoods CheckCompositionNewForRoleFunnel01:
// production wiring-layer packages (cmd/*, examples/*) must mount cells via
// composition.NewForRole — no direct composition.New call (#2278 PR-2).
func TestCOMPOSITION_NEWFORROLE_FUNNEL_01(t *testing.T) {
	t.Parallel()
	Report(t, "COMPOSITION-NEWFORROLE-FUNNEL-01", CheckCompositionNewForRoleFunnel01(t, ConfigForExternalCell{}))
}

// TestCOMPOSITION_NEWFORROLE_FUNNEL_01_RedFixture asserts the detector fires on a
// known-positive composition.New call. The source is written to a temp file and
// parsed (never compiled), so the fixture cannot pollute the production scan.
// Without this, a broken detector would silently pass (anti-vacuity).
func TestCOMPOSITION_NEWFORROLE_FUNNEL_01_RedFixture(t *testing.T) {
	t.Parallel()
	src := "package redfixture\n\n" +
		"import \"" + runtimeCompositionModule + "\"\n\n" +
		"var _ = composition.New\n"
	path := filepath.Join(t.TempDir(), "compose_red.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	line, ok, err := firstQualifiedSelectorLine(path, runtimeCompositionModule, "composition", "New")
	require.NoError(t, err, "parse composition.New RED fixture")
	if !ok {
		t.Error("COMPOSITION-NEWFORROLE-FUNNEL-01 RED fixture: detector found no " +
			"composition.New; firstQualifiedSelectorLine may be broken")
		return
	}
	t.Logf("composition.New RED fixture hit at line %d", line)
}

// TestCOMPOSITION_NEWFORROLE_FUNNEL_01_DoesNotMatchSiblingConstructors asserts the
// scan keys on the exact "New" selector and does NOT flag composition.NewForRole
// (the sanctioned entry) or composition.NewSharedDeps (a different constructor) —
// otherwise the funnel would forbid its own replacement.
func TestCOMPOSITION_NEWFORROLE_FUNNEL_01_DoesNotMatchSiblingConstructors(t *testing.T) {
	t.Parallel()
	src := "package siblingfixture\n\n" +
		"import \"" + runtimeCompositionModule + "\"\n\n" +
		"var _ = composition.NewForRole\n" +
		"var _ = composition.NewSharedDeps\n"
	path := filepath.Join(t.TempDir(), "sibling.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	line, ok, err := firstQualifiedSelectorLine(path, runtimeCompositionModule, "composition", "New")
	require.NoError(t, err, "parse sibling-constructor fixture")
	if ok {
		t.Errorf("COMPOSITION-NEWFORROLE-FUNNEL-01: selector scan must not match "+
			"NewForRole/NewSharedDeps, but matched at line %d", line)
	}
}

// TestCOMPOSITION_NEWFORROLE_FUNNEL_01_AntiVacuity asserts the scanned file set is
// non-empty, so a broken scanner that returns an empty set cannot silently appear
// GREEN.
func TestCOMPOSITION_NEWFORROLE_FUNNEL_01_AntiVacuity(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := scanner.DirsScope(root, compositionNewForRoleWiringRoots).Files()
	require.NoError(t, err, "DirsScope must succeed")
	require.NotEmpty(t, files,
		"COMPOSITION-NEWFORROLE-FUNNEL-01: scanned file set is empty — "+
			"the scanner is broken or compositionNewForRoleWiringRoots point at non-existent dirs")
}

// TestCOMPOSITION_NEWFORROLE_FUNNEL_01_NoDotImportBlindSpot closes the dot-import
// blind spot: a dot-import of runtime/composition would make New a bare ident the
// SelectorExpr scan misses. Asserts no production file in the wiring roots
// dot-imports runtime/composition.
func TestCOMPOSITION_NEWFORROLE_FUNNEL_01_NoDotImportBlindSpot(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := scanner.DirsScope(root, compositionNewForRoleWiringRoots).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := funnelRelSlash(root, path)
		dot, perr := fileDotImportsModule(path, runtimeCompositionModule)
		require.NoError(t, perr)
		if dot {
			findings = append(findings, fmt.Sprintf("%s: dot-import of %s evades COMPOSITION-NEWFORROLE-FUNNEL-01", rel, runtimeCompositionModule))
		}
	}
	assert.Empty(t, findings,
		"dot-importing runtime/composition in a wiring root would make New a bare ident "+
			"and evade the funnel scan; forbidden")
}
