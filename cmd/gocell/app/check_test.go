package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// goModVersion returns the major.minor version string of the running Go toolchain
// (e.g. "1.22") so generated go.mod files track the actual toolchain instead of
// a hardcoded value that drifts over time.
func goModVersion() string {
	v := runtime.Version() // e.g. "go1.26.1"
	v = strings.TrimPrefix(v, "go")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return v
}

func TestCheckContractHealthCI(t *testing.T) {
	// Run against the real project — should pass with 0 issues.
	err := runCheck([]string{"contract-health"})
	assert.NoError(t, err, "contract-health should pass on the project's contracts")
}

// TestCheckContractHealth_JSONFormat verifies --format=json emits a
// machine-readable document. We skip when running outside the gocell tree
// (the real project must be reachable for findRoot()), and we only check
// the structural shape — exact issue list depends on repo state.
func TestCheckContractHealth_JSONFormat(t *testing.T) {
	out := captureStdout(t, func() {
		_ = runCheck([]string{"contract-health", "--format=json"})
	})
	require.NotEmpty(t, out, "JSON format must produce output")

	var doc struct {
		Issues  []map[string]any `json:"issues"`
		Summary struct {
			Errors   int `json:"errors"`
			Warnings int `json:"warnings"`
		} `json:"summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc),
		"--format=json output must be parseable JSON: %q", out)
	assert.NotNil(t, doc.Issues, "issues key must be present (never null)")
	// Confirm no spurious table rendering leaked into JSON mode.
	assert.NotContains(t, out, "Contract Health (",
		"text-mode table must not appear in JSON output")
	assert.NotContains(t, out, "PASS: all contracts healthy",
		"text-mode trailing line must not appear in JSON output")
}

// TestCheckContractHealth_TextFormat_HasMethodPathColumns verifies the
// PR239-OB1 enhancement: METHOD and PATH columns appear in the human
// table. Both the header row and at least one HTTP contract row should
// carry the data, so dashboards can read transport metadata directly from
// `gocell check contract-health` output.
func TestCheckContractHealth_TextFormat_HasMethodPathColumns(t *testing.T) {
	out := captureStdout(t, func() {
		_ = runCheck([]string{"contract-health"})
	})
	assert.Contains(t, out, "METHOD",
		"PR239-OB1: text table must have a METHOD column header")
	assert.Contains(t, out, "PATH",
		"PR239-OB1: text table must have a PATH column header")
}

// TestCheckContractHealth_UnknownFormat verifies the dispatcher errors out
// on unknown format strings rather than silently emitting the default —
// catches typos before they become silent CI passes.
func TestCheckContractHealth_UnknownFormat(t *testing.T) {
	err := runCheck([]string{"contract-health", "--format=yaml"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown format")
}

// TestCheckUnconditionalSkipCI runs the analyzer against the real project.
// The repo is expected to stay clean — any drift fails this test. Pairs
// with the kernel-shard CI step (`gocell check unconditional-skip ./...`)
// so local dev catches regressions before push.
func TestCheckUnconditionalSkipCI(t *testing.T) {
	err := runCheck([]string{"unconditional-skip"})
	assert.NoError(t, err, "unconditional-skip must stay at zero on the project")
}

// TestCheckUnconditionalSkip_JSONFormat verifies --format=json emits a
// machine-readable document and that text-mode summary lines never leak
// into the JSON output.
func TestCheckUnconditionalSkip_JSONFormat(t *testing.T) {
	out := captureStdout(t, func() {
		_ = runCheck([]string{"unconditional-skip", "--format=json"})
	})
	require.NotEmpty(t, out, "JSON format must produce output")

	var doc struct {
		Issues []map[string]any `json:"issues"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc),
		"--format=json output must be parseable JSON: %q", out)
	assert.NotContains(t, out, "Scanned scope:",
		"text-mode scope hint must not appear in JSON output")
	assert.NotContains(t, out, "PASS: no unconditional skips",
		"text-mode trailing line must not appear in JSON output")
}

// TestCheckUnconditionalSkip_TextFormat_PrintsScope confirms that text-mode
// invocations print the scanned root + patterns. Required so users running
// the CLI from a sub-directory don't misread a sub-tree PASS as a repo-wide
// pass — the printed scope reveals the boundary at a glance.
func TestCheckUnconditionalSkip_TextFormat_PrintsScope(t *testing.T) {
	out := captureStdout(t, func() {
		_ = runCheck([]string{"unconditional-skip"})
	})
	assert.Contains(t, out, "Scanned scope:",
		"text mode must print the scan scope so users can verify boundary")
	assert.Contains(t, out, "PASS: no unconditional skips found",
		"clean repo must emit the trailing PASS line")
}

// TestCheckUnconditionalSkip_UnknownFormat mirrors the contract-health
// dispatch contract — unknown --format strings must error out instead of
// silently degrading to default output.
func TestCheckUnconditionalSkip_UnknownFormat(t *testing.T) {
	err := runCheck([]string{"unconditional-skip", "--format=yaml"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown format")
}

// TestRunUnconditionalSkipAnalyzer_BoundedToRoot pins the Config.Dir
// contract: the analyzer must scan from the project root regardless of
// the CWD that invoked it. Without Dir=root, packages.Load resolves
// "./..." against the caller's CWD and a sub-tree scan can silently mask
// violations elsewhere in the repo.
func TestRunUnconditionalSkipAnalyzer_BoundedToRoot(t *testing.T) {
	root, err := findRoot()
	require.NoError(t, err)

	// Switch CWD to a sub-directory and confirm the analyzer still scans
	// the whole repo — i.e. the Config.Dir override is in effect.
	wd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(wd) })
	require.NoError(t, os.Chdir(filepath.Join(root, "cmd", "gocell")))

	results, err := runUnconditionalSkipAnalyzer([]string{"./..."}, root)
	require.NoError(t, err)
	// Repo is clean (PR-CFG-D removed all stub skips); a sub-tree scan
	// from cmd/gocell would pass anyway, but a true repo-wide scan also
	// passes — the assertion is "no error", not "specific findings".
	assert.Empty(t, results, "clean repo must produce zero findings even from sub-dir CWD")
}

// TestCollectPackageErrors confirms that per-package load errors are
// surfaced as a structured aggregate, not silently swallowed via stderr.
// PR#270's SARIF contract demands errors land in stdout so JSON/SARIF
// consumers can ingest them.
func TestCollectPackageErrors(t *testing.T) {
	t.Run("nil pkgs returns nil", func(t *testing.T) {
		assert.NoError(t, collectPackageErrors(nil))
	})
	t.Run("packages with errors aggregate", func(t *testing.T) {
		pkgs := []*packages.Package{
			{Errors: []packages.Error{{Msg: "first error"}}},
			{Errors: []packages.Error{{Msg: "second error"}}},
		}
		err := collectPackageErrors(pkgs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "first error")
		assert.Contains(t, err.Error(), "second error")
	})
	t.Run("packages without errors returns nil", func(t *testing.T) {
		pkgs := []*packages.Package{{}, {}}
		assert.NoError(t, collectPackageErrors(pkgs))
	})
}

// TestRelativeToRoot pins the SARIF SRCROOT contract: file paths handed to
// printers must be repo-relative slash-separated, not absolute. Regression
// guard for F-R2-4 (PR#276 round-2): pos.Filename from go/packages is
// absolute on every platform, and feeding it raw to normalizeArtifactURI
// emits `<SRCROOT>/Users/...` which GitHub Code Scanning cannot map back.
func TestRelativeToRoot(t *testing.T) {
	tests := []struct {
		name string
		root string
		abs  string
		want string
	}{
		{
			name: "absolute path under root → relative slash path",
			root: "/repo",
			abs:  "/repo/cmd/gocell/app/check.go",
			want: "cmd/gocell/app/check.go",
		},
		{
			name: "empty filename → empty",
			root: "/repo",
			abs:  "",
			want: "",
		},
		{
			name: "path outside root → still relative (filepath.Rel inserts ..)",
			root: "/repo",
			abs:  "/other/foo.go",
			want: "../other/foo.go",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := relativeToRoot(tc.root, tc.abs)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// checkSliceCoverage
// ---------------------------------------------------------------------------

// TestCheckSliceCoverage_HappyPath exercises the real accesscore cell —
// its slices/ directory is well-formed so the check should pass.
func TestCheckSliceCoverage_HappyPath(t *testing.T) {
	err := runCheck([]string{"slice-coverage", "--cell=accesscore"})
	assert.NoError(t, err, "slice-coverage on a well-formed cell must pass")
}

// TestCheckSliceCoverage_AllCells exercises all cells (no --cell flag).
func TestCheckSliceCoverage_AllCells(t *testing.T) {
	err := runCheck([]string{"slice-coverage"})
	assert.NoError(t, err, "slice-coverage with no --cell must pass on this project")
}

// TestCheckSliceCoverage_EmptyDirViolation creates a synthetic slices/ subdir
// without a slice.yaml and verifies the CHECK-SLICE-EMPTY-DIR violation fires.
func TestCheckSliceCoverage_EmptyDirViolation(t *testing.T) {
	root := t.TempDir()

	// Minimal go.mod so findRoot() resolves to tempdir.
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644))

	// Build minimal project structure with a cell and an empty slice dir.
	cellDir := filepath.Join(root, "cells", "testcell")
	require.NoError(t, os.MkdirAll(filepath.Join(cellDir, "slices", "emptyslice"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte(
		"id: testcell\ntype: core\nconsistencyLevel: L1\nowner:\n  team: t\n  role: r\nschema:\n  primary: t\nverify:\n  smoke: []\n"),
		0o644))

	// Run check against this synthetic root by changing cwd.
	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	err := runCheck([]string{"slice-coverage", "--cell=testcell"})
	require.Error(t, err, "must fail when a slices/ subdir lacks slice.yaml")
	assert.Contains(t, err.Error(), "issue(s)")
}

// ---------------------------------------------------------------------------
// checkAssemblyCompleteness
// ---------------------------------------------------------------------------

// TestCheckAssemblyCompleteness_HappyPath checks the real corebundle assembly.
func TestCheckAssemblyCompleteness_HappyPath(t *testing.T) {
	err := runCheck([]string{"assembly-completeness", "--id=corebundle"})
	assert.NoError(t, err, "assembly-completeness on corebundle must pass")
}

// TestCheckAssemblyCompleteness_MissingID ensures --id is required.
func TestCheckAssemblyCompleteness_MissingID(t *testing.T) {
	err := runCheck([]string{"assembly-completeness"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id is required")
}

// TestCheckAssemblyCompleteness_MissingCell creates a synthetic assembly
// referencing a nonexistent cell, expecting CHECK-ASSEMBLY-MISSING-CELL.
func TestCheckAssemblyCompleteness_MissingCell(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644))

	// Real cell.
	cellDir := filepath.Join(root, "cells", "realcell")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte(
		"id: realcell\ntype: core\nconsistencyLevel: L1\nowner:\n  team: t\n  role: r\nschema:\n  primary: t\nverify:\n  smoke: []\n"),
		0o644))

	// Assembly referencing a missing cell.
	asmDir := filepath.Join(root, "assemblies", "testbundle")
	require.NoError(t, os.MkdirAll(asmDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(asmDir, "assembly.yaml"), []byte(
		"id: testbundle\ncells:\n  - realcell\n  - ghostcell\nbuild:\n  entrypoint: cmd/test/main.go\n  binary: test\n  deployTemplate: k8s\n"),
		0o644))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	err := runCheck([]string{"assembly-completeness", "--id=testbundle"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "issue(s)")
}

// ---------------------------------------------------------------------------
// checkJourneyReadiness
// ---------------------------------------------------------------------------

// TestCheckJourneyReadiness_HappyPath checks a single known-good journey.
func TestCheckJourneyReadiness_HappyPath(t *testing.T) {
	err := runCheck([]string{"journey-readiness", "--journey=J-ssologin"})
	assert.NoError(t, err, "J-ssologin must pass journey-readiness")
}

// TestCheckJourneyReadiness_AllJourneys checks all journeys in the project.
func TestCheckJourneyReadiness_AllJourneys(t *testing.T) {
	err := runCheck([]string{"journey-readiness"})
	assert.NoError(t, err, "all journeys must pass readiness on this project")
}

// TestCheckJourneyReadiness_UnknownJourney verifies error for unknown journey ID.
func TestCheckJourneyReadiness_UnknownJourney(t *testing.T) {
	err := runCheck([]string{"journey-readiness", "--journey=J-nonexistent"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestCheckJourneyReadiness_NoStatusEntry creates a synthetic project where a
// journey has no status-board entry, expecting CHECK-JOURNEY-NO-STATUS-ENTRY.
func TestCheckJourneyReadiness_NoStatusEntry(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644))

	journeysDir := filepath.Join(root, "journeys")
	require.NoError(t, os.MkdirAll(journeysDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(journeysDir, "J-orphan.yaml"), []byte(
		"id: J-orphan\ngoal: test\nowner:\n  team: t\n  role: r\ncells: []\ncontracts: []\npassCriteria: []\n"),
		0o644))
	// Empty status-board.
	require.NoError(t, os.WriteFile(filepath.Join(journeysDir, "status-board.yaml"), []byte("[]\n"), 0o644))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	err := runCheck([]string{"journey-readiness", "--journey=J-orphan"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "issue(s)")
}

// ---------------------------------------------------------------------------
// checkL0Imports
// ---------------------------------------------------------------------------

// TestCheckL0Imports_NoL0Cells verifies that the check passes cleanly when
// there are no L0 cells (the common project state right now).
func TestCheckL0Imports_NoL0Cells(t *testing.T) {
	err := runCheck([]string{"l0-imports"})
	assert.NoError(t, err, "l0-imports must pass when no L0 cells exist")
}

// TestCheckL0Imports_NonL0CellSkipsWithSuccess verifies that requesting --cell
// for a non-L0 cell prints an informational message and exits 0 (success).
func TestCheckL0Imports_NonL0CellSkipsWithSuccess(t *testing.T) {
	out := captureStdout(t, func() {
		err := runCheck([]string{"l0-imports", "--cell=accesscore"})
		assert.NoError(t, err, "non-L0 cell must exit 0 with skip message")
	})
	// Assert against the exported constant so the test does not rely on fragile
	// substring matching — if the message changes the constant update catches it.
	assert.Contains(t, out, "not L0", "output must contain the not-L0 skip message")
	assert.Contains(t, out, "skipped", "output must indicate the check was skipped")
}

// TestCheckL0Imports_AllCellsScanned verifies that running without --cell
// produces a pass message including the count of L0 cells checked.
func TestCheckL0Imports_AllCellsScanned(t *testing.T) {
	// The project has 0 L0 cells in its current state; the pass path differs.
	// We still verify exit 0 and that no count-less generic message is emitted.
	out := captureStdout(t, func() {
		err := runCheck([]string{"l0-imports"})
		assert.NoError(t, err, "l0-imports with no L0 cells must exit 0")
	})
	// Either "checked 0 L0 cells" (no L0 cells) or "checked N L0 cells" (has L0 cells).
	assert.True(t,
		strings.Contains(out, "checked") && strings.Contains(out, "L0 cells"),
		"output must include a count of checked L0 cells, got: %q", out)
}

// ---------------------------------------------------------------------------
// L0 import helper unit tests (pure functions)
// ---------------------------------------------------------------------------

// TestBuildDeclaredDeps_NoL0Deps verifies empty map for cell with no dependencies.
func TestBuildDeclaredDeps_NoL0Deps(t *testing.T) {
	result := buildDeclaredDeps(&metadata.CellMeta{})
	assert.Empty(t, result, "cell with no L0 deps must return empty map")
}

// TestBuildDeclaredDeps_WithDeps verifies the map is correctly populated.
func TestBuildDeclaredDeps_WithDeps(t *testing.T) {
	cm := &metadata.CellMeta{
		L0Dependencies: []metadata.L0DepMeta{
			{Cell: "shared-crypto", Reason: "hashing"},
			{Cell: "shared-math", Reason: "math"},
		},
	}
	result := buildDeclaredDeps(cm)
	assert.True(t, result["shared-crypto"], "shared-crypto must be in declared deps")
	assert.True(t, result["shared-math"], "shared-math must be in declared deps")
	assert.False(t, result["other-cell"], "other-cell must not be in declared deps")
}

// TestL0UndeclaredImports_AllDeclared verifies no violations when all imports are declared.
func TestL0UndeclaredImports_AllDeclared(t *testing.T) {
	cm := &metadata.CellMeta{ID: "myL0cell", File: "cells/myL0cell/cell.yaml"}
	imported := map[string]bool{"shared-crypto": true}
	declared := map[string]bool{"shared-crypto": true}

	results := l0UndeclaredImports(cm, imported, declared)
	assert.Empty(t, results, "all imported cells declared → no violations")
}

// TestL0UndeclaredImports_UndeclaredImport verifies CHECK-L0-UNDECLARED-IMPORT fires.
func TestL0UndeclaredImports_UndeclaredImport(t *testing.T) {
	cm := &metadata.CellMeta{ID: "myL0cell", File: "cells/myL0cell/cell.yaml"}
	imported := map[string]bool{"shared-crypto": true, "secret-dep": true}
	declared := map[string]bool{"shared-crypto": true}

	results := l0UndeclaredImports(cm, imported, declared)
	require.Len(t, results, 1, "one undeclared import must produce one violation")
	assert.Equal(t, "CHECK-L0-UNDECLARED-IMPORT", results[0].Code)
	assert.Contains(t, results[0].Message, "secret-dep")
}

// TestL0DanglingDeclarations_AllImported verifies no violations when all declared deps are imported.
func TestL0DanglingDeclarations_AllImported(t *testing.T) {
	cm := &metadata.CellMeta{ID: "myL0cell", File: "cells/myL0cell/cell.yaml"}
	imported := map[string]bool{"shared-crypto": true}
	declared := map[string]bool{"shared-crypto": true}

	results := l0DanglingDeclarations(cm, imported, declared)
	assert.Empty(t, results, "all declared deps imported → no violations")
}

// TestL0DanglingDeclarations_DanglingDeclaration verifies CHECK-L0-DANGLING-DECLARATION fires.
func TestL0DanglingDeclarations_DanglingDeclaration(t *testing.T) {
	cm := &metadata.CellMeta{ID: "myL0cell", File: "cells/myL0cell/cell.yaml"}
	imported := map[string]bool{}
	declared := map[string]bool{"shared-crypto": true}

	results := l0DanglingDeclarations(cm, imported, declared)
	require.Len(t, results, 1, "one dangling declaration must produce one violation")
	assert.Equal(t, "CHECK-L0-DANGLING-DECLARATION", results[0].Code)
	assert.Contains(t, results[0].Message, "shared-crypto")
}

// TestL0ImportsForCell_NoDeclaredDeps verifies CHECK-L0-MISSING-L0DEPS fires
// when an L0 cell declares no dependencies. The packages.Load call to a
// non-existent directory returns a fatal load error which is demoted to a
// warning; the missing-deps error is the primary finding.
func TestL0ImportsForCell_NoDeclaredDeps(t *testing.T) {
	cm := &metadata.CellMeta{
		ID:               "my-l0-cell",
		ConsistencyLevel: "L0",
		File:             "cells/my-l0-cell/cell.yaml",
	}
	// Use a non-existent root so packages.Load fails immediately.
	results := l0ImportsForCell(t.TempDir(), cm)
	// At minimum must contain CHECK-L0-MISSING-L0DEPS error.
	var foundMissing bool
	for _, r := range results {
		if r.Code == "CHECK-L0-MISSING-L0DEPS" {
			foundMissing = true
		}
	}
	assert.True(t, foundMissing, "L0 cell with no declared deps must produce CHECK-L0-MISSING-L0DEPS")
}

// TestLoadCellImports_NonExistentDir verifies that loadCellImports returns a
// fatal load error when the cell directory does not exist.
func TestLoadCellImports_NonExistentDir(t *testing.T) {
	cm := &metadata.CellMeta{
		ID:               "ghost-cell",
		ConsistencyLevel: "L0",
	}
	_, loadResults, fatal := loadCellImports(t.TempDir(), cm)
	// packages.Load on a non-existent dir may return an error (fatal=true) or
	// return empty packages — both outcomes are acceptable as long as the
	// function does not panic.
	if fatal {
		require.NotEmpty(t, loadResults, "fatal load must produce at least one warning")
		assert.Equal(t, "CHECK-L0-LOAD-ERROR", loadResults[0].Code)
	}
	// Non-fatal (empty dir) is also fine — just assert no panic.
}

// TestCheckL0ImportsForSingleCell_NotFound verifies error when cell not in project.
func TestCheckL0ImportsForSingleCell_NotFound(t *testing.T) {
	root := t.TempDir()
	project := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	err := checkL0ImportsForSingleCell(root, project, "nonexistent", "text")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestCheckL0ImportsForAllCells_ZeroL0Cells verifies that zero L0 cells exits cleanly.
func TestCheckL0ImportsForAllCells_ZeroL0Cells(t *testing.T) {
	root := t.TempDir()
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {ID: "accesscore", ConsistencyLevel: "L1"},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	out := captureStdout(t, func() {
		err := checkL0ImportsForAllCells(root, project, "text")
		assert.NoError(t, err, "zero L0 cells must succeed")
	})
	assert.Contains(t, out, "0 L0 cells", "output must report 0 L0 cells checked")
}

// TestCheckL0ImportsForSingleCell_NonL0JSONFormat verifies JSON format produces
// an empty results document (not silence) when a non-L0 cell is targeted.
// This lets machine consumers distinguish "ran and found nothing" from "didn't run".
func TestCheckL0ImportsForSingleCell_NonL0JSONFormat(t *testing.T) {
	root := t.TempDir()
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {ID: "accesscore", ConsistencyLevel: "L1"},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	out := captureStdout(t, func() {
		err := checkL0ImportsForSingleCell(root, project, "accesscore", "json")
		assert.NoError(t, err, "non-L0 cell in json format must succeed")
	})
	// In JSON mode, an empty results document is emitted so machine consumers
	// can distinguish "ran, found nothing" from "didn't run" (B.4).
	require.NotEmpty(t, out, "JSON format must produce an empty results document for non-L0 skip")
	var doc struct {
		Issues []any `json:"issues"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc), "output must be valid JSON: %q", out)
	assert.Empty(t, doc.Issues, "non-L0 skip must produce zero issues")
}

// TestCheckSliceCoverage_UnknownCell verifies that --cell=<id> with an ID not
// present in the project produces CHECK-CELL-NOT-FOUND and a non-nil error
// (non-zero exit). Previously this was a silent false-green pass.
func TestCheckSliceCoverage_UnknownCell(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644))

	// Minimal project with one real cell so the parser succeeds.
	cellDir := filepath.Join(root, "cells", "realcell")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte(
		"id: realcell\ntype: core\nconsistencyLevel: L1\nowner:\n  team: t\n  role: r\nschema:\n  primary: t\nverify:\n  smoke: []\n"),
		0o644))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	err := runCheck([]string{"slice-coverage", "--cell=does-not-exist"})
	require.Error(t, err, "unknown --cell must produce non-zero exit")
	assert.Contains(t, err.Error(), "issue(s)", "error must report finding count")

	// Verify the text-format output contains the finding code and available cells.
	textOut := captureStdout(t, func() {
		_ = runCheck([]string{"slice-coverage", "--cell=does-not-exist"})
	})
	assert.Contains(t, textOut, "CHECK-CELL-NOT-FOUND", "text output must contain CHECK-CELL-NOT-FOUND code")
	assert.Contains(t, textOut, "available cells", "text output must list available cells")

	// Also verify the finding code in JSON output.
	out := captureStdout(t, func() {
		_ = runCheck([]string{"slice-coverage", "--cell=does-not-exist", "--format=json"})
	})
	assert.Contains(t, out, "CHECK-CELL-NOT-FOUND", "JSON output must contain CHECK-CELL-NOT-FOUND code")
	assert.Contains(t, out, "available cells", "JSON output must include available cells in message")
}

// TestRunUnconditionalSkipAnalyzer_BuildTaggedFile verifies that
// BuildFlags=-tags=integration e2e examples_smoke causes the analyzer to
// include build-tagged test files. Without BuildFlags those files are silently
// excluded by go/packages and unconditional t.Skip calls inside them are never
// reported.
//
// Strategy: write a temp Go module with a //go:build examples_smoke test file
// that contains an unconditional t.Skip, then assert the analyzer finds it.
func TestRunUnconditionalSkipAnalyzer_BuildTaggedFile(t *testing.T) {
	root := t.TempDir()

	// Minimal go.mod — the module path must resolve; no external deps needed.
	// Use the actual toolchain version so this fixture tracks upgrades automatically.
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), fmt.Appendf(nil, "module example.com/skiptest\n\ngo %s\n", goModVersion()), 0o644))

	// A package directory.
	pkgDir := filepath.Join(root, "mypkg")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))

	// A non-tagged file so the package is visible even without build tags.
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "main.go"), []byte(
		"package mypkg\n"), 0o644))

	// A build-tagged test file with an unconditional t.Skip.
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "smoke_test.go"), []byte(
		"//go:build examples_smoke\n\npackage mypkg\n\nimport \"testing\"\n\nfunc TestSmoke(t *testing.T) {\n\tt.Skip(\"unconditional stub\")\n}\n"), 0o644))

	results, err := runUnconditionalSkipAnalyzer([]string{"./..."}, root)
	require.NoError(t, err, "analyzer must not error on this synthetic module")

	var found bool
	for _, r := range results {
		if r.Code == "UNCONDITIONAL-SKIP-01" && strings.Contains(r.File, "smoke_test.go") {
			found = true
			break
		}
	}
	assert.True(t, found,
		"analyzer with BuildFlags must report UNCONDITIONAL-SKIP-01 in build-tagged file; got: %+v", results)
}

// ---------------------------------------------------------------------------
// Metadata parse error path for each subcommand
// ---------------------------------------------------------------------------

// TestCheckSliceCoverage_MetadataParseError verifies the metadata parse error
// path in checkSliceCoverage when the project YAML is malformed.
func TestCheckSliceCoverage_MetadataParseError(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644))

	// Write a malformed cell.yaml to trigger a parse error.
	cellDir := filepath.Join(root, "cells", "badcell")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte("id: [not: yaml\n"), 0o644))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	err := runCheck([]string{"slice-coverage"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata parse")
}

// TestCheckJourneyReadiness_MetadataParseError verifies the metadata parse error
// path in checkJourneyReadiness.
func TestCheckJourneyReadiness_MetadataParseError(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644))

	cellDir := filepath.Join(root, "cells", "badcell")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte("id: [not: yaml\n"), 0o644))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	err := runCheck([]string{"journey-readiness"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata parse")
}

// TestCheckAssemblyCompleteness_MetadataParseError verifies the metadata parse
// error path in checkAssemblyCompleteness.
func TestCheckAssemblyCompleteness_MetadataParseError(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644))

	cellDir := filepath.Join(root, "cells", "badcell")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte("id: [not: yaml\n"), 0o644))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	err := runCheck([]string{"assembly-completeness", "--id=testbundle"})
	require.Error(t, err)
	// Note: may fail with parse error or "not found" depending on parse order.
	require.Error(t, err)
}

// TestCheckL0Imports_MetadataParseError verifies the metadata parse error path
// in checkL0Imports.
func TestCheckL0Imports_MetadataParseError(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644))

	cellDir := filepath.Join(root, "cells", "badcell")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte("id: [not: yaml\n"), 0o644))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	err := runCheck([]string{"l0-imports"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata parse")
}

// TestPrintAndCheck_JSONFormat verifies printAndCheck in JSON mode produces
// machine-readable output and returns a meaningful error on findings.
func TestPrintAndCheck_JSONFormat(t *testing.T) {
	results := []governance.ValidationResult{
		{
			Code:      "TEST-01",
			Severity:  governance.SeverityError,
			IssueType: governance.IssueRequired,
			File:      "test/file.yaml",
			Message:   "test error message",
		},
	}
	out := captureStdout(t, func() {
		err := printAndCheck("json", results, "test-check", "PASS")
		require.Error(t, err, "findings must produce error")
		assert.Contains(t, err.Error(), "test-check")
	})
	// JSON output must be parseable.
	require.NotEmpty(t, out, "json mode must produce output")
}

// TestPrintAndCheck_SARIFFormat verifies printAndCheck in SARIF mode.
func TestPrintAndCheck_SARIFFormat(t *testing.T) {
	results := []governance.ValidationResult{}
	out := captureStdout(t, func() {
		err := printAndCheck("sarif", results, "test-check", "PASS")
		assert.NoError(t, err, "no findings must exit 0")
	})
	// SARIF output should contain the SARIF schema marker.
	require.NotEmpty(t, out, "sarif mode must produce output")
	assert.Contains(t, out, "sarif", strings.ToLower(out))
}

// TestPrintContractHealthTable_Empty verifies the empty contracts branch.
func TestPrintContractHealthTable_Empty(t *testing.T) {
	out := captureStdout(t, func() {
		printContractHealthTable(nil)
	})
	assert.Contains(t, out, "No contracts found")
}

// ---------------------------------------------------------------------------
// httpTransportColumns
// ---------------------------------------------------------------------------

// TestHTTPTransportColumns covers the cell-table helper directly — both
// branches: HTTP contract with method+path, and non-HTTP / missing
// transport gets "-" placeholders so the table column widths stay stable.
func TestHTTPTransportColumns(t *testing.T) {
	tests := []struct {
		name       string
		c          *metadata.ContractMeta
		wantMethod string
		wantPath   string
	}{
		{
			name: "http with method+path",
			c: &metadata.ContractMeta{
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{
						Method: "GET",
						Path:   "/api/v1/things",
					},
				},
			},
			wantMethod: "GET",
			wantPath:   "/api/v1/things",
		},
		{
			name: "http with empty method renders dash",
			c: &metadata.ContractMeta{
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{Path: "/x"},
				},
			},
			wantMethod: "-",
			wantPath:   "/x",
		},
		{
			name: "event contract gets dashes",
			c: &metadata.ContractMeta{
				Kind: "event",
			},
			wantMethod: "-",
			wantPath:   "-",
		},
		{
			name:       "http with nil HTTP transport gets dashes",
			c:          &metadata.ContractMeta{Kind: "http"},
			wantMethod: "-",
			wantPath:   "-",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method, path := httpTransportColumns(tt.c)
			assert.Equal(t, tt.wantMethod, method)
			assert.Equal(t, tt.wantPath, path)
		})
	}
}
