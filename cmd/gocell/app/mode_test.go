package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- validate --fail-fast ---
//
// TestFormatResultsFailFast_FirstError / NoErrors previously called
// formatResultsFailFast directly. After PR-A10 the renderer moved into
// cmd/gocell/app/printers; coverage of "first-error-only, no banner, no
// summary" lives there as TestText_PrintFailFast and
// TestText_PrintFailFast_NoErrors. The integration tests below still
// exercise the wiring end-to-end through runValidate.

// fixtureFailFastError writes a temp project containing a cell.yaml with
// `type: INVALID` so the FMT-02 governance rule fires a SeverityError. Used
// as the deterministic error fixture across all (format × outcome) cases.
func fixtureFailFastError(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))
	cellDir := filepath.Join(dir, "cells", "bad")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	badCellYAML := "id: bad\ntype: INVALID\nconsistencyLevel: L1\n" +
		"owner:\n  team: squad\n  role: cell-owner\n" +
		"schema:\n  primary: cell_bad\nverify:\n  smoke:\n    - smoke.bad.startup\n"
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"),
		[]byte(badCellYAML), 0o644))
	return dir
}

// fixtureFailFastWarnings writes a temp project whose only metadata issue is
// REF-16 (assembly missing generated/boundary.yaml — SeverityWarning). REF-11
// is satisfied via a stub cmd dir so REF-16 stays the lone diagnostic.
func fixtureFailFastWarnings(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))
	asmDir := filepath.Join(dir, "assemblies", "warnasm")
	require.NoError(t, os.MkdirAll(asmDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cmd", "warnasm"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(asmDir, "assembly.yaml"),
		[]byte("id: warnasm\ncells: []\nbuild:\n  entrypoint: cmd/warnasm\n  binary: warnasm\n  deployTemplate: deploy.yaml\n"), 0o644))
	return dir
}

// fixtureFailFastClean writes a temp project with only go.mod — no cells, no
// assemblies, no journeys — the "truly clean" baseline.
func fixtureFailFastClean(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/empty\n"), 0o644))
	return dir
}

// TestRunValidate_FailFast collapses 4 historical fragmented fail-fast tests
// (TrimsToFirstError / OnWarningsOnly / NoErrors / ReturnsError) into a
// single 9-case matrix covering every (format × outcome) cell that the
// machine-consumer contract exposes.
//
// Why matrix:
//   - 3 formats (text / json / sarif) — every CI consumer uses one
//   - 3 outcomes (error / warnings-only / clean) — fail-fast renders all 3
//   - error+JSON and error+SARIF were entirely uncovered before this commit;
//     a regression that produced malformed JSON or empty SARIF results on
//     fail-fast error path would have shipped silently
//
// Format-specific assertions are intentionally structural (top-level keys +
// payload array shape), not full schema validation. The full SARIF 2.1.0
// schema and the gocell JSON shape are pinned by clean-path tests in
// dispatch_test.go::TestDispatch_ValidateFormats — this matrix layers
// fail-fast/outcome coverage on top, not duplicate schema coverage.
func TestRunValidate_FailFast(t *testing.T) {
	cases := []struct {
		name    string
		format  string // empty = no --format flag (text default)
		fixture func(*testing.T) string
		wantErr bool
		// asserter receives stdout + non-nil runErr (when wantErr=true) and
		// performs format-specific structural checks.
		asserter func(t *testing.T, stdout string, runErr error)
	}{
		// --- text ---
		{
			name:    "text-error",
			fixture: fixtureFailFastError,
			wantErr: true,
			asserter: func(t *testing.T, stdout string, runErr error) {
				assert.Contains(t, stdout, "FMT-02", "first error code must be printed")
				assert.NotContains(t, stdout, "Validation complete:", "fail-fast on error must not emit summary")
				assert.NotContains(t, stdout, "WARNINGS (", "fail-fast on error must not emit warnings banner")
				assert.NotContains(t, stdout, "ERRORS (", "fail-fast on error must not emit multi-error banner")
				assert.Contains(t, runErr.Error(), "validation failed:", "runValidate err must carry 'validation failed:' prefix")
			},
		},
		{
			name:    "text-warnings",
			fixture: fixtureFailFastWarnings,
			wantErr: false,
			asserter: func(t *testing.T, stdout string, _ error) {
				assert.Contains(t, stdout, "WARNINGS (", "warnings banner must surface even under fail-fast")
				assert.Contains(t, stdout, "Validation complete:", "summary line carries the warning count")
				assert.NotContains(t, stdout, "OK: no errors.", "OK line is reserved for truly clean runs")
			},
		},
		{
			name:    "text-clean",
			fixture: fixtureFailFastClean,
			wantErr: false,
			asserter: func(t *testing.T, stdout string, _ error) {
				assert.Equal(t, "OK: no errors.\n", stdout, "fail-fast clean output is the single-line ack")
			},
		},

		// --- json ---
		{
			name:    "json-error",
			format:  "json",
			fixture: fixtureFailFastError,
			wantErr: true,
			asserter: func(t *testing.T, stdout string, runErr error) {
				doc := unmarshalJSONDoc(t, stdout)
				require.Len(t, doc.Issues, 1, "fail-fast on error must emit exactly one issue (the first error)")
				assert.Equal(t, "FMT-02", doc.Issues[0].Code, "issue code must match the triggered rule")
				assert.Equal(t, "error", doc.Issues[0].Severity, "issue severity must be 'error'")
				assert.Contains(t, runErr.Error(), "validation failed:", "runValidate err propagates regardless of format")
			},
		},
		{
			name:    "json-warnings",
			format:  "json",
			fixture: fixtureFailFastWarnings,
			wantErr: false,
			asserter: func(t *testing.T, stdout string, _ error) {
				doc := unmarshalJSONDoc(t, stdout)
				assert.NotNil(t, doc.Issues, "issues array must be present (never null)")
				assert.NotEmpty(t, doc.Issues, "warnings outcome must surface ≥1 issue")
				for _, iss := range doc.Issues {
					assert.NotEqual(t, "error", iss.Severity, "warnings-only outcome must not contain error-severity issues")
				}
			},
		},
		{
			name:    "json-clean",
			format:  "json",
			fixture: fixtureFailFastClean,
			wantErr: false,
			asserter: func(t *testing.T, stdout string, _ error) {
				doc := unmarshalJSONDoc(t, stdout)
				assert.NotNil(t, doc.Issues, "issues key must be present (never null) on clean fail-fast")
				assert.Empty(t, doc.Issues, "clean fail-fast must emit an empty issues array")
			},
		},

		// --- sarif ---
		{
			name:    "sarif-error",
			format:  "sarif",
			fixture: fixtureFailFastError,
			wantErr: true,
			asserter: func(t *testing.T, stdout string, runErr error) {
				log := unmarshalSARIFLog(t, stdout)
				assert.Equal(t, "2.1.0", log.Version)
				require.Len(t, log.Runs, 1)
				assert.Equal(t, "gocell", log.Runs[0].Tool.Driver.Name)
				require.Len(t, log.Runs[0].Results, 1, "fail-fast on error must emit exactly one SARIF result")
				assert.Equal(t, "FMT-02", log.Runs[0].Results[0].RuleID, "SARIF ruleId must match the triggered rule")
				assert.Contains(t, runErr.Error(), "validation failed:")
			},
		},
		{
			name:    "sarif-warnings",
			format:  "sarif",
			fixture: fixtureFailFastWarnings,
			wantErr: false,
			asserter: func(t *testing.T, stdout string, _ error) {
				log := unmarshalSARIFLog(t, stdout)
				require.Len(t, log.Runs, 1)
				assert.NotEmpty(t, log.Runs[0].Results, "warnings outcome must surface ≥1 SARIF result")
			},
		},
		{
			name:    "sarif-clean",
			format:  "sarif",
			fixture: fixtureFailFastClean,
			wantErr: false,
			asserter: func(t *testing.T, stdout string, _ error) {
				log := unmarshalSARIFLog(t, stdout)
				assert.Equal(t, "2.1.0", log.Version)
				require.Len(t, log.Runs, 1)
				assert.Equal(t, "gocell", log.Runs[0].Tool.Driver.Name)
				assert.NotNil(t, log.Runs[0].Results, "results array must be present even when empty")
				assert.Empty(t, log.Runs[0].Results, "clean fail-fast must emit empty results")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.fixture(t)
			args := []string{"--root", dir, "--fail-fast"}
			if tc.format != "" {
				args = append(args, "--format="+tc.format)
			}

			var gotErr error
			stdout := captureStdout(t, func() {
				gotErr = runValidate(args)
			})

			if tc.wantErr {
				require.Error(t, gotErr, "fail-fast must return non-nil error for this outcome")
			} else {
				require.NoError(t, gotErr, "non-error outcome must return nil; stdout=%q", stdout)
			}
			tc.asserter(t, stdout, gotErr)
		})
	}
}

// jsonValidateDoc mirrors the gocell printer's JSON output shape. Lives next
// to the test that consumes it so a printer change forces an explicit edit
// here rather than silently shifting consumer expectations.
type jsonValidateDoc struct {
	Issues  []jsonValidateIssue `json:"issues"`
	Summary struct {
		Errors   int `json:"errors"`
		Warnings int `json:"warnings"`
	} `json:"summary"`
}

type jsonValidateIssue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
}

func unmarshalJSONDoc(t *testing.T, stdout string) jsonValidateDoc {
	t.Helper()
	var doc jsonValidateDoc
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc),
		"stdout must be valid JSON: %q", stdout)
	return doc
}

// sarifLog mirrors the SARIF 2.1.0 fields the gocell printer emits — a tight
// subset of the spec sufficient to assert tool identity + result shape.
type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool struct {
		Driver struct {
			Name string `json:"name"`
		} `json:"driver"`
	} `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifResult struct {
	RuleID string `json:"ruleId"`
}

func unmarshalSARIFLog(t *testing.T, stdout string) sarifLog {
	t.Helper()
	var log sarifLog
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(stdout)), &log),
		"stdout must be valid SARIF JSON: %q", stdout)
	return log
}

// --- validate --strict ---

// writeKebabSlice writes a minimal cells/{cell}/slices/{dir}/slice.yaml that
// will trip FMT-16 in strict mode because the directory contains '-'. Returns
// the project root. Used by the --strict tests below.
func writeKebabSlice(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))
	cellDir := filepath.Join(dir, "cells", "accesscore")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	accCellYAML := "id: accesscore\ntype: core\nconsistencyLevel: L1\n" +
		"owner:\n  team: squad\n  role: cell-owner\n" +
		"schema:\n  primary: cell_accesscore\n" +
		"verify:\n  smoke:\n    - smoke.accesscore.startup\n"
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"),
		[]byte(accCellYAML), 0o644))
	sliceDir := filepath.Join(cellDir, "slices", "session-login") // kebab dir
	require.NoError(t, os.MkdirAll(sliceDir, 0o755))
	sessionSliceYAML := "id: session-login\nbelongsToCell: accesscore\n" +
		"contractUsages: []\nverify:\n  unit:\n    - unit.session-login.service\n" +
		"  contract: []\nallowedFiles:\n  - cells/accesscore/slices/session-login/**\n"
	require.NoError(t, os.WriteFile(filepath.Join(sliceDir, "slice.yaml"),
		[]byte(sessionSliceYAML), 0o644))
	return dir
}

// Strict full mode: FMT-16 fires on a kebab-case slice directory; the base
// pass produces no errors, so the summary shows the FMT-16 error and the
// command returns a non-nil error.
func TestRunValidate_Strict_Full_ErrorsOnKebabDir(t *testing.T) {
	dir := writeKebabSlice(t)

	var gotErr error
	out := captureStdout(t, func() {
		gotErr = runValidate([]string{"--root", dir, "--strict"})
	})
	require.Error(t, gotErr, "strict full must return error when FMT-16 fires")
	assert.Contains(t, gotErr.Error(), "validation failed")
	assert.Contains(t, out, "FMT-16", "full-mode output must report FMT-16 code")
	assert.Contains(t, out, "Validation complete:", "full-mode must print summary")
}

// Strict + fail-fast: base pass is clean, so FMT-16 is appended and becomes
// the single reported error. Validates that a strict-only violation is caught
// when no standard-rule error takes precedence.
func TestRunValidate_StrictFailFast_StrictOnlyError(t *testing.T) {
	dir := writeKebabSlice(t)

	var gotErr error
	out := captureStdout(t, func() {
		gotErr = runValidate([]string{"--root", dir, "--strict", "--fail-fast"})
	})
	require.Error(t, gotErr, "strict+fail-fast must surface FMT-16")
	assert.Contains(t, gotErr.Error(), "validation failed: FMT-16")
	assert.NotContains(t, out, "Validation complete:",
		"fail-fast must not emit summary line")
}

// Strict + fail-fast: when a base-pass error (e.g. FMT-02 invalid type) is
// present alongside a kebab directory, the base error short-circuits and
// FMT-16 must NOT appear — this is the whole reason ValidateStrictFailFast
// gates FMT-16/17 behind HasErrors(results).
func TestRunValidate_StrictFailFast_BaseErrorWinsOverStrict(t *testing.T) {
	dir := writeKebabSlice(t)
	// Corrupt the cell.yaml so standard rules fire an error before FMT-16 runs.
	invalidCellYAML := "id: accesscore\ntype: INVALID\nconsistencyLevel: L1\n" +
		"owner:\n  team: squad\n  role: cell-owner\n" +
		"schema:\n  primary: cell_accesscore\n" +
		"verify:\n  smoke:\n    - smoke.accesscore.startup\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "accesscore", "cell.yaml"),
		[]byte(invalidCellYAML), 0o644))

	var gotErr error
	out := captureStdout(t, func() {
		gotErr = runValidate([]string{"--root", dir, "--strict", "--fail-fast"})
	})
	require.Error(t, gotErr, "strict+fail-fast must return error when base pass fails")
	assert.NotContains(t, gotErr.Error(), "FMT-16",
		"base error must short-circuit FMT-16/17 under fail-fast")
	assert.NotContains(t, out, "FMT-16",
		"fail-fast output must not include FMT-16 when base pass already failed")
}

// writeKebabCellID writes a cell whose directory and yaml id are both
// kebab-case ("access-core"). The base pass REF-04 (id ↔ dir match) and
// VERIFY-05 (smoke ref ↔ cell id match) therefore pass, and FMT-16 + FMT-C1
// are the only strict-only errors. The fixture intentionally trips both
// FMT-16 and FMT-C1 — REF-04 already catches the dir/id-divergence
// half-migrations the FMT-C1 doc-string mentions, so the natural fixture
// shape that exercises FMT-C1 in isolation does not exist; layering
// FMT-C1 on top of FMT-16 is defense-in-depth.
func writeKebabCellID(t *testing.T) string {
	t.Helper()
	dir := setupProject(t, "cells/access-core") // kebab dir matching id
	kebabCellYAML := "id: access-core\ntype: core\nconsistencyLevel: L1\n" +
		"owner:\n  team: squad\n  role: cell-owner\n" +
		"schema:\n  primary: cell_access_core\n" +
		"verify:\n  smoke:\n    - smoke.access-core.startup\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "access-core", "cell.yaml"),
		[]byte(kebabCellYAML), 0o644))
	return dir
}

// writeAllowedFilesMismatch writes a no-dash slice whose allowedFiles[0] does
// not match its on-disk directory. Triggers FMT-17 only (FMT-14 is satisfied
// because allowedFiles is non-empty; FMT-16 stays silent).
func writeAllowedFilesMismatch(t *testing.T) string {
	t.Helper()
	dir := setupProject(t, "cells/accesscore", "cells/accesscore/slices/validdir")
	accCoreYAML := "id: accesscore\ntype: core\nconsistencyLevel: L1\n" +
		"owner:\n  team: squad\n  role: cell-owner\n" +
		"schema:\n  primary: cell_accesscore\n" +
		"verify:\n  smoke:\n    - smoke.accesscore.startup\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "accesscore", "cell.yaml"),
		[]byte(accCoreYAML), 0o644))
	// allowedFiles points to a different slice directory ("wrongdir") — FMT-17 fires.
	validDirYAML := "id: validdir\nbelongsToCell: accesscore\n" +
		"contractUsages: []\nverify:\n  unit:\n    - unit.validdir.service\n" +
		"  contract: []\nallowedFiles:\n  - cells/accesscore/slices/wrongdir/**\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "cells", "accesscore", "slices", "validdir", "slice.yaml"),
		[]byte(validDirYAML), 0o644))
	return dir
}

// writeKebabAssemblyID writes a no-dash assembly directory whose declared id
// contains '-'. Triggers FMT-A1 in strict mode (in addition to whatever
// base findings the metadata layer surfaces for the dir/id mismatch).
func writeKebabAssemblyID(t *testing.T) string {
	t.Helper()
	// REF-11 verifies build.entrypoint exists; the cmd/myasm stub keeps that
	// rule quiet so the focus stays on FMT-A1.
	dir := setupProject(t, "assemblies/myasm", "cmd/myasm")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "assemblies", "myasm", "assembly.yaml"),
		[]byte("id: my-asm\ncells: []\nbuild:\n  entrypoint: cmd/myasm\n  binary: myasm\n  deployTemplate: deploy.yaml\n"), 0o644))
	return dir
}

// TestRunValidate_Strict_DetectsKebabCellID locks in FMT-C1 in strict full
// mode: a kebab-case cell id is rejected only by ValidateStrict(true). The
// fixture has a kebab directory (necessary because REF-04 enforces id ↔
// dir match, so an id-only kebab is impossible to construct without a
// base error pre-empting strict), so FMT-16 fires alongside FMT-C1 — that
// is the defense-in-depth pair the rule was designed for, and the
// assertion below verifies both rules light up.
func TestRunValidate_Strict_DetectsKebabCellID(t *testing.T) {
	dir := writeKebabCellID(t)

	var gotErr error
	out := captureStdout(t, func() {
		gotErr = runValidate([]string{"--root", dir, "--strict"})
	})
	require.Error(t, gotErr, "strict must return error when FMT-C1 fires on kebab cell id")
	assert.Contains(t, out, "FMT-C1", "full-mode output must report FMT-C1 code")
	assert.Contains(t, out, "FMT-16", "FMT-16 must also fire — kebab dir is the natural co-trigger")
}

// TestRunValidate_Strict_DetectsAllowedFilesMismatch locks in FMT-17: a
// slice whose allowedFiles[0] does not match its on-disk directory is only
// caught in strict mode. FMT-14 passes (allowedFiles is non-empty) so this
// covers the gap between "allowedFiles declared" and "allowedFiles correct".
func TestRunValidate_Strict_DetectsAllowedFilesMismatch(t *testing.T) {
	dir := writeAllowedFilesMismatch(t)

	var gotErr error
	out := captureStdout(t, func() {
		gotErr = runValidate([]string{"--root", dir, "--strict"})
	})
	require.Error(t, gotErr, "strict must return error when FMT-17 fires on allowedFiles mismatch")
	assert.Contains(t, out, "FMT-17", "full-mode output must report FMT-17 code")
	assert.NotContains(t, out, "FMT-16", "FMT-16 must stay silent — directory itself is no-dash")
}

// TestRunValidate_Strict_DetectsKebabAssemblyID locks in FMT-A1: assembly
// ids leak into binary names and deploy templates, so kebab is rejected
// even when the directory is clean.
func TestRunValidate_Strict_DetectsKebabAssemblyID(t *testing.T) {
	dir := writeKebabAssemblyID(t)

	var gotErr error
	out := captureStdout(t, func() {
		gotErr = runValidate([]string{"--root", dir, "--strict"})
	})
	require.Error(t, gotErr, "strict must return error when FMT-A1 fires on kebab assembly id")
	assert.Contains(t, out, "FMT-A1", "full-mode output must report FMT-A1 code")
}

// TestRunValidate_Default_IgnoresStrictOnlyRules is the regression guard
// for the strict gate itself: every fixture that trips FMT-16 / 17 / C1 /
// A1 in strict mode must remain silent under default mode. Without this
// case a refactor that accidentally promoted a strict rule to a base rule
// would slip through (CI gates would still pass, but `gocell validate`
// without --strict would no longer be the documented "default-permissive"
// surface).
//
// The fixtures here may emit unrelated base findings (e.g. REF-16 warning,
// or REF-* errors stemming from minimal scaffolding); the test deliberately
// does NOT assert "no error returned" because the contract being verified
// is narrower: regardless of base findings, no FMT-16/17/C1/A1 line may
// appear in default output. `out` is logged on failure so a future drift
// in base rules surfaces the offending code.
func TestRunValidate_Default_IgnoresStrictOnlyRules(t *testing.T) {
	cases := []struct {
		name string
		fix  func(*testing.T) string
	}{
		{"kebabSliceDir_FMT16", writeKebabSlice},
		{"kebabCellID_FMTC1", writeKebabCellID},
		{"allowedFilesMismatch_FMT17", writeAllowedFilesMismatch},
		{"kebabAssemblyID_FMTA1", writeKebabAssemblyID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.fix(t)

			out := captureStdout(t, func() {
				_ = runValidate([]string{"--root", dir})
			})
			for _, code := range []string{"FMT-16", "FMT-17", "FMT-C1", "FMT-A1"} {
				if assert.NotContains(t, out, code, "%s must stay silent under default mode", code) {
					continue
				}
				t.Logf("default-mode validate output:\n%s", out)
			}
		})
	}
}

// --- scaffold --dry-run ---
//
// These tests drive runScaffoldWithRoot directly, bypassing runScaffold's
// findRoot() / cwd dependency. Previously each test did os.Chdir(tempDir),
// which serializes the whole test binary (F-SEC-03). With an explicit root
// parameter, t.TempDir() is isolated by design.

// setupProject writes go.mod and any extra subdirs inside a fresh tempdir,
// returning the dir. Keeps the boilerplate out of each test body.
func setupProject(t *testing.T, extraDirs ...string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))
	for _, d := range extraDirs {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
	}
	return dir
}

func TestRunScaffoldCell_DryRun_NoFileWritten(t *testing.T) {
	dir := setupProject(t, "cells")

	out := captureStdout(t, func() {
		err := runScaffoldWithRoot(dir,
			[]string{"cell", "--id=dry-cell", "--team=squad", "--role=cell-owner", "--dry-run"})
		require.NoError(t, err)
	})

	_, statErr := os.Stat(filepath.Join(dir, "cells", "dry-cell", "cell.yaml"))
	assert.True(t, os.IsNotExist(statErr), "dry-run must not create cell.yaml")

	assert.Contains(t, out, "dry-run", "output must mark dry-run mode")
	assert.Contains(t, out, "cells/dry-cell/cell.yaml",
		"dry-run must report the path that would be written")
	assert.NotContains(t, out, "Created cell", "dry-run must not emit a 'Created cell' line")
}

func TestRunScaffoldSlice_DryRun_NoFileWritten(t *testing.T) {
	dir := setupProject(t, "cells/parent-cell/slices")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "parent-cell", "cell.yaml"),
		[]byte("id: parent-cell\ntype: core\n"), 0o644))

	out := captureStdout(t, func() {
		err := runScaffoldWithRoot(dir,
			[]string{"slice", "--id=dryslice", "--cell=parent-cell", "--dry-run"})
		require.NoError(t, err)
	})

	_, statErr := os.Stat(filepath.Join(dir,
		"cells", "parent-cell", "slices", "dryslice", "slice.yaml"))
	assert.True(t, os.IsNotExist(statErr), "dry-run must not create slice.yaml")
	assert.NotContains(t, out, "Created slice", "dry-run must not emit a 'Created slice' line")
}

func TestRunScaffoldContract_DryRun_NoFileWritten(t *testing.T) {
	dir := setupProject(t, "contracts")

	out := captureStdout(t, func() {
		err := runScaffoldWithRoot(dir, []string{
			"contract",
			"--id=http.dry.test.v1", "--kind=http", "--owner=some-cell", "--dry-run",
		})
		require.NoError(t, err)
	})

	_, statErr := os.Stat(filepath.Join(dir,
		"contracts", "http", "dry", "test", "v1", "contract.yaml"))
	assert.True(t, os.IsNotExist(statErr), "dry-run must not create contract.yaml")
	assert.NotContains(t, out, "Created contract", "dry-run must not emit a 'Created contract' line")
}

func TestRunScaffoldJourney_DryRun_NoFileWritten(t *testing.T) {
	dir := setupProject(t, "journeys")

	out := captureStdout(t, func() {
		err := runScaffoldWithRoot(dir, []string{
			"journey",
			"--id=J-dry", "--goal=test goal", "--team=squad", "--cells=a,b", "--dry-run",
		})
		require.NoError(t, err)
	})

	_, statErr := os.Stat(filepath.Join(dir, "journeys", "J-dry.yaml"))
	assert.True(t, os.IsNotExist(statErr), "dry-run must not create journey file")
	assert.NotContains(t, out, "Created journey", "dry-run must not emit a 'Created journey' line")
}

func TestRunValidate_Strict_DetectsManualOnlyActiveJourney(t *testing.T) {
	dir := setupProject(t, "cells/platform", "journeys")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "platform", "cell.yaml"), []byte(`id: platform
type: core
consistencyLevel: L1
owner:
  team: squad
  role: cell-owner
schema:
  primary: cell_platform
verify:
  smoke:
    - smoke.platform.startup
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "journeys", "J-manual.yaml"), []byte(`id: J-manual
goal: manual-only journey
lifecycle: active
owner:
  team: squad
  role: journey-owner
cells:
  - platform
contracts: []
passCriteria:
  - text: manual signoff
    mode: manual
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "journeys", "status-board.yaml"), []byte(`- journeyId: J-manual
  state: todo
  risk: low
  blocker: ""
  updatedAt: 2026-04-27
`), 0o644))

	var gotErr error
	out := captureStdout(t, func() {
		gotErr = runValidate([]string{"--root", dir, "--strict"})
	})
	require.Error(t, gotErr)
	assert.Contains(t, out, "VERIFY-06")
}

func TestRunValidate_Strict_DetectsStaleActiveJourneyCheckRef(t *testing.T) {
	dir := setupProject(t, "cells/platform", "journeys", "tests/integration")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "platform", "cell.yaml"), []byte(`id: platform
type: core
consistencyLevel: L1
owner:
  team: squad
  role: cell-owner
schema:
  primary: cell_platform
verify:
  smoke:
    - smoke.platform.startup
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "journeys", "J-stale.yaml"), []byte(`id: J-stale
goal: stale auto check
lifecycle: active
owner:
  team: squad
  role: journey-owner
cells:
  - platform
contracts: []
passCriteria:
  - text: stale target
    mode: auto
    checkRef: journey.J-stale.missing
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "journeys", "status-board.yaml"), []byte(`- journeyId: J-stale
  state: todo
  risk: low
  blocker: ""
  updatedAt: 2026-04-27
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tests", "integration", "journey_test.go"), []byte(`package integration
import "testing"
func TestOtherJourney(t *testing.T) {}
`), 0o644))

	var gotErr error
	out := captureStdout(t, func() {
		gotErr = runValidate([]string{"--root", dir, "--strict"})
	})
	require.Error(t, gotErr)
	assert.Contains(t, out, "VERIFY-06")
	assert.Contains(t, out, "J-stale")
}

func TestRunVerifyJourneyActive_DetectsManualOnlyActiveJourney(t *testing.T) {
	dir := setupProject(t, "cells/platform", "journeys")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "platform", "cell.yaml"), []byte(`id: platform
type: core
consistencyLevel: L1
owner:
  team: squad
  role: cell-owner
schema:
  primary: cell_platform
verify:
  smoke:
    - smoke.platform.startup
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "journeys", "J-manual.yaml"), []byte(`id: J-manual
goal: manual-only journey
lifecycle: active
owner:
  team: squad
  role: journey-owner
cells:
  - platform
contracts: []
passCriteria:
  - text: manual signoff
    mode: manual
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "journeys", "status-board.yaml"), []byte(`- journeyId: J-manual
  state: todo
  risk: low
  blocker: ""
  updatedAt: 2026-04-27
`), 0o644))

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(orig))
	})

	var gotErr error
	out := captureStdout(t, func() {
		gotErr = runVerify([]string{"journey", "--active"})
	})
	require.Error(t, gotErr)
	assert.Contains(t, gotErr.Error(), "verify journey --active: FAILED")
	assert.Contains(t, out, "Verify active journeys: FAILED")
	assert.Contains(t, out, "active journey has no auto checkRef")
}

// Dry-run must still fail-fast on invalid opts — this is the whole point: CI
// pre-commit hooks can call `scaffold ... --dry-run` and stop on bad inputs
// without leaving partial files behind.
func TestRunScaffoldCell_DryRun_StillValidatesOpts(t *testing.T) {
	dir := setupProject(t, "cells")

	err := runScaffoldWithRoot(dir,
		[]string{"cell", "--id=no-team", "--dry-run"})
	require.Error(t, err, "missing --team must fail even in dry-run")
}

// Dry-run must still detect existing target path — reporting "would create"
// silently over an existing file would be misleading.
func TestRunScaffoldCell_DryRun_DetectsConflict(t *testing.T) {
	dir := setupProject(t)
	target := filepath.Join(dir, "cells", "conflict-cell")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "cell.yaml"),
		[]byte("id: conflict-cell\n"), 0o644))

	err := runScaffoldWithRoot(dir, []string{
		"cell",
		"--id=conflict-cell", "--team=squad", "--role=cell-owner", "--dry-run",
	})
	require.Error(t, err, "dry-run must still surface conflicts")
}

func TestRunScaffoldSlice_DryRun_DetectsConflict(t *testing.T) {
	dir := setupProject(t)
	sliceDir := filepath.Join(dir, "cells", "my-cell", "slices", "conflict-slice")
	require.NoError(t, os.MkdirAll(sliceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "my-cell", "cell.yaml"),
		[]byte("id: my-cell\ntype: core\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sliceDir, "slice.yaml"),
		[]byte("id: conflict-slice\n"), 0o644))

	err := runScaffoldWithRoot(dir, []string{
		"slice",
		"--id=conflict-slice", "--cell=my-cell", "--dry-run",
	})
	require.Error(t, err, "dry-run must still surface slice conflicts")
}

func TestRunScaffoldContract_DryRun_DetectsConflict(t *testing.T) {
	dir := setupProject(t)
	contractDir := filepath.Join(dir, "contracts", "http", "conflict", "api", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contractDir, "contract.yaml"),
		[]byte("id: http.conflict.api.v1\n"), 0o644))

	err := runScaffoldWithRoot(dir, []string{
		"contract",
		"--id=http.conflict.api.v1", "--kind=http", "--owner=some-cell", "--dry-run",
	})
	require.Error(t, err, "dry-run must still surface contract conflicts")
}

func TestRunScaffoldJourney_DryRun_DetectsConflict(t *testing.T) {
	dir := setupProject(t, "journeys")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "journeys", "J-conflict.yaml"),
		[]byte("id: J-conflict\n"), 0o644))

	err := runScaffoldWithRoot(dir, []string{
		"journey",
		"--id=conflict", "--goal=test goal", "--team=squad", "--cells=a", "--dry-run",
	})
	require.Error(t, err, "dry-run must still surface journey conflicts")
}
