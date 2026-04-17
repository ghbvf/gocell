package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- validate --fail-fast ---

// formatResultsFailFast must print only the first error encountered, and then
// stop — no "ERRORS (N):" banner, no warnings, no trailing summary. Errors
// win over warnings regardless of slice order.
func TestFormatResultsFailFast_FirstError(t *testing.T) {
	results := []governance.ValidationResult{
		{Code: "ADV-01", Severity: governance.SeverityWarning, Message: "warn first"},
		{Code: "REF-01", Severity: governance.SeverityError, Message: "first error"},
		{Code: "REF-02", Severity: governance.SeverityError, Message: "second error"},
	}
	out := captureStdout(t, func() { formatResultsFailFast(results) })
	assert.Contains(t, out, "REF-01")
	assert.Contains(t, out, "first error")
	assert.NotContains(t, out, "REF-02", "second error must not be printed")
	assert.NotContains(t, out, "second error")
	assert.NotContains(t, out, "warn first", "warnings must not be printed in fail-fast")
	assert.NotContains(t, out, "ERRORS (", "fail-fast mode skips the banner")
}

// When no errors exist, fail-fast acts as a no-op (no spurious output). The
// caller is responsible for deciding success; formatResultsFailFast itself
// only prints when an error is present.
func TestFormatResultsFailFast_NoErrors(t *testing.T) {
	results := []governance.ValidationResult{
		{Code: "ADV-01", Severity: governance.SeverityWarning, Message: "just a warning"},
	}
	out := captureStdout(t, func() { formatResultsFailFast(results) })
	assert.Empty(t, strings.TrimSpace(out), "no output expected when no errors: %q", out)
}

// runValidate --fail-fast on the live project: if errors exist it returns
// error; the printed output must not include a "Validation complete:" summary
// or a "WARNINGS (" banner. We only assert format invariants here — whether
// there are errors or not depends on the repo state at test time.
func TestRunValidate_FailFast_FormatInvariants(t *testing.T) {
	root, err := findRoot()
	require.NoError(t, err)

	out := captureStdout(t, func() {
		_ = runValidate([]string{"--root", root, "--fail-fast"})
	})
	assert.NotContains(t, out, "Validation complete:",
		"fail-fast must not emit summary line")
	assert.NotContains(t, out, "WARNINGS (",
		"fail-fast must not emit warnings banner")
}

// --- scaffold --dry-run ---

func TestRunScaffoldCell_DryRun_NoFileWritten(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cells"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	out := captureStdout(t, func() {
		err := runScaffold([]string{"cell", "--id=dry-cell", "--team=squad", "--dry-run"})
		require.NoError(t, err)
	})

	_, statErr := os.Stat(filepath.Join(dir, "cells", "dry-cell", "cell.yaml"))
	assert.True(t, os.IsNotExist(statErr), "dry-run must not create cell.yaml")

	assert.Contains(t, out, "dry-run", "output must mark dry-run mode")
	assert.Contains(t, out, "cells/dry-cell/cell.yaml",
		"dry-run must report the path that would be written")
	assert.NotContains(t, out, "Created cell:", "dry-run must not emit a 'Created cell:' line")
}

func TestRunScaffoldSlice_DryRun_NoFileWritten(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cells", "parent-cell", "slices"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "parent-cell", "cell.yaml"),
		[]byte("id: parent-cell\ntype: core\n"), 0o644))

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	out := captureStdout(t, func() {
		err := runScaffold([]string{"slice", "--id=dry-slice", "--cell=parent-cell", "--dry-run"})
		require.NoError(t, err)
	})

	_, statErr := os.Stat(filepath.Join(dir, "cells", "parent-cell", "slices", "dry-slice", "slice.yaml"))
	assert.True(t, os.IsNotExist(statErr), "dry-run must not create slice.yaml")
	assert.NotContains(t, out, "Created slice:", "dry-run must not emit a 'Created slice:' line")
}

func TestRunScaffoldContract_DryRun_NoFileWritten(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "contracts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	out := captureStdout(t, func() {
		err := runScaffold([]string{"contract",
			"--id=http.dry.test.v1", "--kind=http", "--owner=some-cell", "--dry-run"})
		require.NoError(t, err)
	})

	_, statErr := os.Stat(filepath.Join(dir, "contracts", "http", "dry", "test", "v1", "contract.yaml"))
	assert.True(t, os.IsNotExist(statErr), "dry-run must not create contract.yaml")
	assert.NotContains(t, out, "Created contract:", "dry-run must not emit a 'Created contract:' line")
}

func TestRunScaffoldJourney_DryRun_NoFileWritten(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "journeys"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	out := captureStdout(t, func() {
		err := runScaffold([]string{"journey",
			"--id=J-dry", "--goal=test goal", "--team=squad", "--cells=a,b", "--dry-run"})
		require.NoError(t, err)
	})

	_, statErr := os.Stat(filepath.Join(dir, "journeys", "J-dry.yaml"))
	assert.True(t, os.IsNotExist(statErr), "dry-run must not create journey file")
	assert.NotContains(t, out, "Created journey:", "dry-run must not emit a 'Created journey:' line")
}

// TestRunValidate_FailFast_ReturnsError checks that runValidate with --fail-fast
// returns a non-nil error whose message contains "validation failed:" when there
// are governance-rule violations. We build a minimal temp project with a cell.yaml
// that parses successfully but triggers an FMT-02 error (invalid cell type).
func TestRunValidate_FailFast_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	// Minimal go.mod so findRoot succeeds when --root is explicit.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))
	// A cell.yaml with a valid id but an invalid type — triggers FMT-02 (SeverityError).
	cellDir := filepath.Join(dir, "cells", "bad-cell")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"),
		[]byte("id: bad-cell\ntype: INVALID\nconsistencyLevel: L2\nowner:\n  team: squad\n  role: cell-owner\n"), 0o644))

	var gotErr error
	_ = captureStdout(t, func() {
		gotErr = runValidate([]string{"--root", dir, "--fail-fast"})
	})
	require.Error(t, gotErr, "runValidate with --fail-fast must return error when validation errors exist")
	assert.Contains(t, gotErr.Error(), "validation failed:",
		"error message must contain 'validation failed:'")
}

// Dry-run must still fail-fast on invalid opts — this is the whole point: CI
// pre-commit hooks can call `scaffold ... --dry-run` and stop on bad inputs
// without leaving partial files behind.
func TestRunScaffoldCell_DryRun_StillValidatesOpts(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cells"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	err := runScaffold([]string{"cell", "--id=no-team", "--dry-run"})
	require.Error(t, err, "missing --team must fail even in dry-run")
}

// Dry-run must still detect existing target path — reporting "would create"
// silently over an existing file would be misleading.
func TestRunScaffoldCell_DryRun_DetectsConflict(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cells", "conflict-cell")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "cell.yaml"),
		[]byte("id: conflict-cell\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	err := runScaffold([]string{"cell",
		"--id=conflict-cell", "--team=squad", "--dry-run"})
	require.Error(t, err, "dry-run must still surface conflicts")
}

// TestRunScaffoldSlice_DryRun_DetectsConflict mirrors the cell conflict test
// for slice scaffolding.
func TestRunScaffoldSlice_DryRun_DetectsConflict(t *testing.T) {
	dir := t.TempDir()
	// Create parent cell and a pre-existing slice target.
	sliceDir := filepath.Join(dir, "cells", "my-cell", "slices", "conflict-slice")
	require.NoError(t, os.MkdirAll(sliceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "my-cell", "cell.yaml"),
		[]byte("id: my-cell\ntype: core\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sliceDir, "slice.yaml"),
		[]byte("id: conflict-slice\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	err := runScaffold([]string{"slice",
		"--id=conflict-slice", "--cell=my-cell", "--dry-run"})
	require.Error(t, err, "dry-run must still surface slice conflicts")
}

// TestRunScaffoldContract_DryRun_DetectsConflict mirrors the cell conflict test
// for contract scaffolding.
func TestRunScaffoldContract_DryRun_DetectsConflict(t *testing.T) {
	dir := t.TempDir()
	contractDir := filepath.Join(dir, "contracts", "http", "conflict", "api", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contractDir, "contract.yaml"),
		[]byte("id: http.conflict.api.v1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	err := runScaffold([]string{"contract",
		"--id=http.conflict.api.v1", "--kind=http", "--owner=some-cell", "--dry-run"})
	require.Error(t, err, "dry-run must still surface contract conflicts")
}

// TestRunScaffoldJourney_DryRun_DetectsConflict mirrors the cell conflict test
// for journey scaffolding.
func TestRunScaffoldJourney_DryRun_DetectsConflict(t *testing.T) {
	dir := t.TempDir()
	journeyDir := filepath.Join(dir, "journeys")
	require.NoError(t, os.MkdirAll(journeyDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(journeyDir, "J-conflict.yaml"),
		[]byte("id: J-conflict\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	err := runScaffold([]string{"journey",
		"--id=conflict", "--goal=test goal", "--team=squad", "--cells=a", "--dry-run"})
	require.Error(t, err, "dry-run must still surface journey conflicts")
}
