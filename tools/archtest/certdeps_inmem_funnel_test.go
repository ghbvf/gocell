//go:build archtest

// INVARIANT: CERTDEPS-INMEM-FUNNEL-01
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

// TestCERTDEPS_INMEM_FUNNEL_01 dogfoods CheckCertdepsInmemFunnel01: no wiring root
// (cmd/*, cellmodules/*, examples/*) may construct softca directly — all cert
// wiring routes through cellmodules/certdeps.Resolve, whose demo branch is the
// sole sanctioned site (#2302).
func TestCERTDEPS_INMEM_FUNNEL_01(t *testing.T) {
	t.Parallel()
	Report(t, "CERTDEPS-INMEM-FUNNEL-01", CheckCertdepsInmemFunnel01(t, ConfigForExternalCell{}))
}

// TestCERTDEPS_INMEM_FUNNEL_01_RedFixture asserts the detector fires on a
// known-positive softca.NewDevCA reference. The source is written to a temp file
// and parsed (never compiled), so it cannot pollute the production scan. Without
// this, a broken detector would silently pass (anti-vacuity).
func TestCERTDEPS_INMEM_FUNNEL_01_RedFixture(t *testing.T) {
	t.Parallel()
	src := "package redfixture\n\n" +
		"import \"" + certdepsSoftcaModule + "\"\n\n" +
		"var _ = softca.NewDevCA\n"
	path := filepath.Join(t.TempDir(), "softca_red.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	line, ok, err := firstQualifiedSelectorLine(path, certdepsSoftcaModule, "softca", "NewDevCA")
	require.NoError(t, err, "parse softca RED fixture")
	if !ok {
		t.Error("CERTDEPS-INMEM-FUNNEL-01 RED fixture: detector found no softca.NewDevCA; " +
			"firstQualifiedSelectorLine may be broken")
		return
	}
	t.Logf("softca RED fixture hit at line %d", line)
}

// TestCERTDEPS_INMEM_FUNNEL_01_ScanCore_SyntheticTree drives scanCertdepsInmemFunnel
// over a synthetic temp tree to prove (1) the scan finds a violation under a
// scanned root and (2) the sanctioned-dir allowlist suppresses certdeps's own
// softca call — i.e. the allowlist is load-bearing, not vacuous.
func TestCERTDEPS_INMEM_FUNNEL_01_ScanCore_SyntheticTree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Violation: a composition root constructs softca directly.
	writeSyntheticFile(t, root, "cmd/foo/main.go",
		"package main\n\nimport \""+certdepsSoftcaModule+"\"\n\nvar _ = softca.NewSoftCA\n")

	// Sanctioned: certdeps's own demo branch — must be suppressed by the allowlist.
	writeSyntheticFile(t, root, "cellmodules/certdeps/certdeps.go",
		"package certdeps\n\nimport \""+certdepsSoftcaModule+"\"\n\nvar _ = softca.NewDevCA\n")

	diags, err := scanCertdepsInmemFunnel(root)
	require.NoError(t, err)
	require.Len(t, diags, 1, "exactly the cmd/foo violation; certdeps must be allowlisted")
	assert.Equal(t, "cmd/foo/main.go", diags[0].Rel)
	assert.Contains(t, diags[0].Message, "softca.NewSoftCA")
}

// TestCERTDEPS_INMEM_FUNNEL_01_NoDotImportBlindSpot closes the dot-import blind
// spot: a dot-import of adapters/softca would make the constructors bare idents
// the SelectorExpr scan misses. Asserts no production file in the scanned wiring
// roots dot-imports softca.
func TestCERTDEPS_INMEM_FUNNEL_01_NoDotImportBlindSpot(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := scanner.DirsScope(root, certdepsInmemScannedRoots).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		dot, perr := fileDotImportsModule(path, certdepsSoftcaModule)
		require.NoError(t, perr)
		if dot {
			findings = append(findings, fmt.Sprintf("%s: dot-import of %s evades CERTDEPS-INMEM-FUNNEL-01",
				funnelRelSlash(root, path), certdepsSoftcaModule))
		}
	}
	assert.Empty(t, findings,
		"dot-importing adapters/softca in a wiring root would make the constructors bare idents "+
			"and evade the funnel scan; forbidden")
}

// writeSyntheticFile writes src to root/rel, creating parent dirs. Used only by
// the synthetic-tree anti-vacuity test.
func writeSyntheticFile(t *testing.T, root, rel, src string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(src), 0o600))
}
