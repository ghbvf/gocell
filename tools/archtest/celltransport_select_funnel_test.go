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
