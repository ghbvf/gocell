//go:build archtest

// INVARIANT: SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01
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

// TestSAGA_PROJECTION_DEPS_INMEM_FUNNEL_01 dogfoods CheckSagaProjectionDepsInmemFunnel01:
// no wiring-layer file (cmd/*, cellmodules/*, examples/*) outside the sanctioned
// resolver cellmodules/sagaprojectiondeps may call distlock.NewInProcessDriver
// directly — the in-process single-pod locker is reachable only through
// sagaprojectiondeps.Resolve's demo branch (#1391 / #2060).
func TestSAGA_PROJECTION_DEPS_INMEM_FUNNEL_01(t *testing.T) {
	t.Parallel()
	Report(t, "SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01", CheckSagaProjectionDepsInmemFunnel01(t, ConfigForExternalCell{}))
}

// TestSAGA_PROJECTION_DEPS_INMEM_FUNNEL_01_RedFixture asserts the detector fires
// on a known-positive distlock.NewInProcessDriver call. The source is written to
// a temp file and parsed (never compiled), so it cannot pollute the real scan.
// Without this, a broken detector would silently pass (anti-vacuity).
func TestSAGA_PROJECTION_DEPS_INMEM_FUNNEL_01_RedFixture(t *testing.T) {
	t.Parallel()
	src := "package redfixture\n\n" +
		"import \"" + distlockModule + "\"\n\n" +
		"var _ = distlock.NewInProcessDriver(nil)\n"
	path := filepath.Join(t.TempDir(), "inproc_red.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	line, ok, err := firstQualifiedSelectorLine(path, distlockModule, "distlock", "NewInProcessDriver")
	require.NoError(t, err, "parse RED fixture")
	if !ok {
		t.Error("SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01 RED fixture: detector found no " +
			"distlock.NewInProcessDriver; firstQualifiedSelectorLine may be broken")
		return
	}
	t.Logf("RED fixture hit at line %d", line)
}

// TestSAGA_PROJECTION_DEPS_INMEM_FUNNEL_01_NoDotImportBlindSpot closes the
// dot-import blind spot: a dot-import of runtime/distlock would make
// NewInProcessDriver a bare ident the SelectorExpr scan misses. Asserts no scanned
// wiring file dot-imports runtime/distlock.
func TestSAGA_PROJECTION_DEPS_INMEM_FUNNEL_01_NoDotImportBlindSpot(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := scanner.DirsScope(root, sagaProjectionDepsScannedRoots).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := funnelRelSlash(root, path)
		dot, perr := fileDotImportsModule(path, distlockModule)
		require.NoError(t, perr)
		if dot {
			findings = append(findings, fmt.Sprintf("%s: dot-import of %s evades SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01", rel, distlockModule))
		}
	}
	assert.Empty(t, findings,
		"dot-importing runtime/distlock in a wiring root would make NewInProcessDriver a bare "+
			"ident and evade the funnel scan; forbidden")
}
