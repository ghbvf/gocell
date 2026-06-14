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

// writeSagaFunnelFixtureFile writes a syntactically valid Go file at
// root/rel that imports runtime/distlock and calls distlock.NewInProcessDriver.
// firstQualifiedSelectorLine parses (never compiles) the file, so it need not
// resolve against the real module — only the import path + selector matter.
func writeSagaFunnelFixtureFile(t *testing.T, root, rel, pkg string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o750))
	src := "package " + pkg + "\n\n" +
		"import \"" + distlockModule + "\"\n\n" +
		"var _ = distlock.NewInProcessDriver(nil)\n"
	require.NoError(t, os.WriteFile(abs, []byte(src), 0o600))
}

// TestSAGA_PROJECTION_DEPS_INMEM_FUNNEL_01_ScanCore_SyntheticTree exercises the
// FULL scan core (scanSagaProjectionDepsInmemFunnel) — DirsScope + allowlist skip
// + Diagnostic assembly — against a synthetic t.TempDir tree, NOT just the
// selector helper. This makes the funnel's anti-vacuity genuine: it proves the
// scope finds offending files under a scanned root AND that the sanctioned-dir
// allowlist suppresses an identical violation inside cellmodules/sagaprojectiondeps.
func TestSAGA_PROJECTION_DEPS_INMEM_FUNNEL_01_ScanCore_SyntheticTree(t *testing.T) {
	t.Parallel()

	t.Run("violating file under scanned root yields a diagnostic", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		const rel = "cmd/foo/x.go"
		writeSagaFunnelFixtureFile(t, root, rel, "foo")

		diags, err := scanSagaProjectionDepsInmemFunnel(root)
		require.NoError(t, err)
		require.Len(t, diags, 1, "a distlock.NewInProcessDriver call under cmd/ must produce exactly one diagnostic")
		assert.Equal(t, rel, diags[0].Rel)
		assert.Equal(t, sagaProjectionDepsFunnelMessage, diags[0].Message)
		assert.Positive(t, diags[0].Line, "diagnostic must carry the offending line")
	})

	t.Run("identical violation inside the sanctioned dir is allowlisted", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		// The sanctioned resolver dir is allowed to construct the in-process driver.
		writeSagaFunnelFixtureFile(t, root, sagaProjectionDepsSanctionedDir+"/y.go", "sagaprojectiondeps")

		diags, err := scanSagaProjectionDepsInmemFunnel(root)
		require.NoError(t, err)
		assert.Empty(t, diags, "the sanctioned resolver dir must be allowlisted (zero diagnostics)")
	})

	t.Run("both present: only the unsanctioned file is reported", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeSagaFunnelFixtureFile(t, root, "examples/bar/z.go", "bar")
		writeSagaFunnelFixtureFile(t, root, sagaProjectionDepsSanctionedDir+"/y.go", "sagaprojectiondeps")

		diags, err := scanSagaProjectionDepsInmemFunnel(root)
		require.NoError(t, err)
		require.Len(t, diags, 1, "only the examples/ violation must be reported; the sanctioned dir is skipped")
		assert.Equal(t, "examples/bar/z.go", diags[0].Rel)
	})
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
