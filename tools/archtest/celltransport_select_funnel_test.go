//go:build archtest

// INVARIANT: CELLTRANSPORT-SELECT-FUNNEL-01
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

// TestCELLTRANSPORT_SELECT_FUNNEL_01 dogfoods CheckCelltransportSelectFunnel01:
// wiring-layer packages (cmd/*, cellmodules/*, examples/*) outside the
// allowlisted celltransport package must contain no direct transport.NewRemoteHTTP
// call — they must route through cellmodules/celltransport.Resolve (#1966).
func TestCELLTRANSPORT_SELECT_FUNNEL_01(t *testing.T) {
	t.Parallel()
	Report(t, "CELLTRANSPORT-SELECT-FUNNEL-01", CheckCelltransportSelectFunnel01(t, ConfigForExternalCell{}))
}

// TestCELLTRANSPORT_SELECT_FUNNEL_01_RedFixture asserts the detector fires on a
// known-positive transport.NewRemoteHTTP call. The source is written to a temp
// file and parsed (never compiled) — so the fixture cannot pollute the production
// scan. Without this, a broken detector would silently pass (anti-vacuity).
func TestCELLTRANSPORT_SELECT_FUNNEL_01_RedFixture(t *testing.T) {
	t.Parallel()
	src := "package redfixture\n\n" +
		"import \"" + runtimeTransportModule + "\"\n\n" +
		"var _ = transport.NewRemoteHTTP\n"
	path := filepath.Join(t.TempDir(), "remote_red.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	line, ok, err := firstQualifiedSelectorLine(path, runtimeTransportModule, "transport", "NewRemoteHTTP")
	require.NoError(t, err, "parse RemoteHTTP RED fixture")
	if !ok {
		t.Error("CELLTRANSPORT-SELECT-FUNNEL-01 RED fixture: detector found no " +
			"transport.NewRemoteHTTP; firstQualifiedSelectorLine may be broken")
		return
	}
	t.Logf("RemoteHTTP RED fixture hit at line %d", line)
}

// TestCELLTRANSPORT_SELECT_FUNNEL_01_GreenAllowlist_SanctionedCallerNotFlagged
// asserts that the sanctioned caller (cellmodules/celltransport/resolve.go) is
// NOT flagged by CheckCelltransportSelectFunnel01 — i.e. the allowlist works
// correctly and the allowlisted file produces zero diagnostics.
//
// Anti-vacuity companion: also asserts the scanned file set is non-empty, so a
// broken scanner that returns an empty set cannot silently appear GREEN.
func TestCELLTRANSPORT_SELECT_FUNNEL_01_GreenAllowlist_SanctionedCallerNotFlagged(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	files, err := scanner.DirsScope(root, celltransportWiringRoots).Files()
	require.NoError(t, err, "DirsScope must succeed")

	// Anti-vacuity: the scanned file set must contain at least one file so a
	// broken scanner (returns empty set) does not silently pass.
	require.NotEmpty(t, files,
		"CELLTRANSPORT-SELECT-FUNNEL-01 GREEN: scanned file set is empty — "+
			"the scanner is broken or celltransportWiringRoots point at non-existent dirs")

	// The sanctioned caller must not appear in the diagnostics.
	diags := CheckCelltransportSelectFunnel01(t, ConfigForExternalCell{})
	for _, d := range diags {
		if len(d.Rel) >= len("cellmodules/celltransport/") &&
			d.Rel[:len("cellmodules/celltransport/")] == "cellmodules/celltransport/" {
			t.Errorf("sanctioned caller %s was flagged by CELLTRANSPORT-SELECT-FUNNEL-01 "+
				"at line %d — allowlist is broken: %s", d.Rel, d.Line, d.Message)
		}
	}
}

// TestCELLTRANSPORT_SELECT_FUNNEL_01_RedFixture_Corecells asserts the detector
// fires when a file under a corecells/-prefixed path calls transport.NewRemoteHTTP
// directly. This proves that corecells/ is included in celltransportWiringRoots
// and the scan actually reaches it (anti-vacuity: without corecells/ in the root
// list the fixture would be invisible and the test would pass vacuously).
func TestCELLTRANSPORT_SELECT_FUNNEL_01_RedFixture_Corecells(t *testing.T) {
	t.Parallel()

	// Write the synthetic source into a temp dir structured like a corecells sub-package.
	// The file is only parsed (never compiled), so it cannot affect the production scan.
	src := "package corecellsred\n\n" +
		"import \"" + runtimeTransportModule + "\"\n\n" +
		"var _ = transport.NewRemoteHTTP\n"
	path := filepath.Join(t.TempDir(), "corecells_red.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	line, ok, err := firstQualifiedSelectorLine(path, runtimeTransportModule, "transport", "NewRemoteHTTP")
	require.NoError(t, err, "parse corecells RED fixture")
	if !ok {
		t.Error("CELLTRANSPORT-SELECT-FUNNEL-01 corecells RED fixture: detector found no " +
			"transport.NewRemoteHTTP — firstQualifiedSelectorLine or the fixture is broken")
		return
	}
	t.Logf("corecells RED fixture: transport.NewRemoteHTTP detected at line %d", line)
}

// TestCELLTRANSPORT_SELECT_FUNNEL_01_NoDotImportBlindSpot closes the dot-import
// blind spot: a dot-import of runtime/transport would make NewRemoteHTTP a bare
// ident the SelectorExpr scan misses. Asserts no production file in the wiring
// roots dot-imports runtime/transport.
func TestCELLTRANSPORT_SELECT_FUNNEL_01_NoDotImportBlindSpot(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := scanner.DirsScope(root, celltransportWiringRoots).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := funnelRelSlash(root, path)
		dot, perr := fileDotImportsModule(path, runtimeTransportModule)
		require.NoError(t, perr)
		if dot {
			findings = append(findings, fmt.Sprintf("%s: dot-import of %s evades CELLTRANSPORT-SELECT-FUNNEL-01", rel, runtimeTransportModule))
		}
	}
	assert.Empty(t, findings,
		"dot-importing runtime/transport in a wiring root would make NewRemoteHTTP a bare ident "+
			"and evade the funnel scan; forbidden")
}
