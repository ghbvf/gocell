package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/governance"
)

func TestPrintUsage(t *testing.T) {
	// Should not panic.
	PrintUsage()
}

func TestFindRoot(t *testing.T) {
	// Save and restore working directory.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if chErr := os.Chdir(orig); chErr != nil {
			t.Logf("cleanup: failed to restore working directory: %v", chErr)
		}
	})

	// findRoot should find the go.mod in the project root (walk up from
	// current test dir to find it).
	root, err := findRoot()
	if err != nil {
		t.Fatalf("findRoot() error: %v", err)
	}

	// Verify go.mod exists at the found root.
	gomod := filepath.Join(root, "go.mod")
	if _, statErr := os.Stat(gomod); statErr != nil {
		t.Fatalf("findRoot() returned %q but go.mod not found there: %v", root, statErr)
	}
}

func TestReadModule(t *testing.T) {
	root, err := findRoot()
	if err != nil {
		t.Fatalf("findRoot() error: %v", err)
	}

	mod, err := readModule(root)
	if err != nil {
		t.Fatalf("readModule() error: %v", err)
	}

	if mod != "github.com/ghbvf/gocell" {
		t.Errorf("readModule() = %q, want %q", mod, "github.com/ghbvf/gocell")
	}
}

func TestReadModuleNotFound(t *testing.T) {
	// Use a temp directory without go.mod.
	dir := t.TempDir()
	_, err := readModule(dir)
	if err == nil {
		t.Error("readModule() should return error for directory without go.mod")
	}
}

func TestFormatResults(t *testing.T) {
	// Capture stdout by redirecting.
	results := []governance.ValidationResult{
		{
			Code:     "REF-01",
			Severity: governance.SeverityError,
			File:     "cells/test/cell.yaml",
			Field:    "id",
			Message:  "cell ID is required",
		},
		{
			Code:     "ADV-01",
			Severity: governance.SeverityWarning,
			File:     "cells/test/cell.yaml",
			Field:    "owner.role",
			Message:  "owner role is recommended",
		},
	}

	// formatResults writes to stdout; just verify it doesn't panic.
	// For more thorough testing, we'd redirect stdout, but that's
	// complex for a CLI test. Verify the helper functions instead.
	formatResults(results)
	formatResults(nil) // empty case
}

func TestFormatResultsContainsCodeAndMessage(t *testing.T) {
	results := []governance.ValidationResult{
		{
			Code:     "TEST-01",
			Severity: governance.SeverityError,
			File:     "test.yaml",
			Field:    "field",
			Message:  "test message here",
		},
	}

	// Capture output using a pipe.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	formatResults(results)

	if closeErr := w.Close(); closeErr != nil {
		t.Logf("pipe close: %v", closeErr)
	}
	os.Stdout = old

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "TEST-01") {
		t.Errorf("output should contain code TEST-01, got: %s", output)
	}
	if !strings.Contains(output, "test message here") {
		t.Errorf("output should contain message, got: %s", output)
	}
}

func TestFormatTestList(t *testing.T) {
	tests := []struct {
		input []string
		want  string
	}{
		{nil, "(none)"},
		{[]string{}, "(none)"},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a, b"},
	}

	for _, tt := range tests {
		got := formatTestList(tt.input)
		if got != tt.want {
			t.Errorf("formatTestList(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCommands(t *testing.T) {
	// Verify all expected commands are registered.
	expected := []string{"validate", "scaffold", "generate", "check", "verify"}
	for _, name := range expected {
		if _, ok := commands[name]; !ok {
			t.Errorf("command %q not registered in commands map", name)
		}
	}
}

// --- Command function tests ---

func TestRunValidate(t *testing.T) {
	root, err := findRoot()
	if err != nil {
		t.Fatal(err)
	}
	// Run validate with explicit root. It may return an error if there are
	// validation errors in the project, but it should not panic.
	// We only assert no panic; validation errors are expected.
	t.Logf("runValidate result: %v", runValidate([]string{"--root", root}))
}

func TestRunValidateNoRoot(t *testing.T) {
	// Running without --root should auto-detect.
	// May succeed or fail with validation errors; just ensure no panic.
	t.Logf("runValidate result: %v", runValidate([]string{}))
}

func TestRunScaffoldNoArgs(t *testing.T) {
	err := runScaffold([]string{})
	if err == nil {
		t.Error("scaffold with no args should return error")
	}
}

func TestRunScaffoldUnknownType(t *testing.T) {
	err := runScaffold([]string{"unknown"})
	if err == nil {
		t.Error("scaffold unknown type should return error")
	}
}

func TestRunScaffoldCellMissingFlags(t *testing.T) {
	err := runScaffold([]string{"cell"})
	if err == nil {
		t.Error("scaffold cell without --id should return error")
	}
}

func TestRunScaffoldCellMissingTeam(t *testing.T) {
	err := runScaffold([]string{"cell", "--id=test-cell"})
	if err == nil {
		t.Error("scaffold cell without --team should return error")
	}
}

func TestRunScaffoldSliceMissingFlags(t *testing.T) {
	err := runScaffold([]string{"slice"})
	if err == nil {
		t.Error("scaffold slice without --id should return error")
	}
	err = runScaffold([]string{"slice", "--id=test-slice"})
	if err == nil {
		t.Error("scaffold slice without --cell should return error")
	}
}

func TestRunScaffoldContractMissingFlags(t *testing.T) {
	err := runScaffold([]string{"contract"})
	if err == nil {
		t.Error("scaffold contract without --id should return error")
	}
	err = runScaffold([]string{"contract", "--id=test.contract.v1"})
	if err == nil {
		t.Error("scaffold contract without --kind should return error")
	}
	err = runScaffold([]string{"contract", "--id=test.contract.v1", "--kind=http"})
	if err == nil {
		t.Error("scaffold contract without --owner should return error")
	}
}

func TestRunScaffoldJourneyMissingFlags(t *testing.T) {
	err := runScaffold([]string{"journey"})
	if err == nil {
		t.Error("scaffold journey without --id should return error")
	}
	err = runScaffold([]string{"journey", "--id=test"})
	if err == nil {
		t.Error("scaffold journey without --goal should return error")
	}
	err = runScaffold([]string{"journey", "--id=test", "--goal=test"})
	if err == nil {
		t.Error("scaffold journey without --team should return error")
	}
	err = runScaffold([]string{"journey", "--id=test", "--goal=test", "--team=team"})
	if err == nil {
		t.Error("scaffold journey without --cells should return error")
	}
}

func TestRunGenerateNoArgs(t *testing.T) {
	err := runGenerate([]string{})
	if err == nil {
		t.Error("generate with no args should return error")
	}
}

func TestRunGenerateUnknownType(t *testing.T) {
	err := runGenerate([]string{"unknown"})
	if err == nil {
		t.Error("generate unknown type should return error")
	}
}

func TestRunGenerateIndexes(t *testing.T) {
	err := runGenerate([]string{"indexes"})
	if err == nil {
		t.Error("generate indexes should return not-implemented error")
	}
}

func TestRunGenerateBoundaries(t *testing.T) {
	err := runGenerate([]string{"boundaries"})
	if err == nil {
		t.Error("generate boundaries should return not-implemented error")
	}
}

func TestRunGenerateAssemblyMissingID(t *testing.T) {
	err := runGenerate([]string{"assembly"})
	if err == nil {
		t.Error("generate assembly without --id should return error")
	}
}

func TestRunCheckNoArgs(t *testing.T) {
	err := runCheck([]string{})
	if err == nil {
		t.Error("check with no args should return error")
	}
}

func TestRunCheckUnknownType(t *testing.T) {
	err := runCheck([]string{"unknown"})
	if err == nil {
		t.Error("check unknown type should return error")
	}
}

func TestRunCheckContractHealth(t *testing.T) {
	err := runCheck([]string{"contract-health"})
	// Should succeed (may find 0 or N contracts).
	if err != nil {
		t.Errorf("check contract-health should succeed, got: %v", err)
	}
}

func TestRunCheckPlaceholders(t *testing.T) {
	placeholders := []string{"slice-coverage", "assembly-completeness", "journey-readiness", "l0-imports"}
	for _, name := range placeholders {
		err := runCheck([]string{name})
		if err == nil {
			t.Errorf("check %s should return not-implemented error", name)
		}
	}
}

func TestRunVerifyNoArgs(t *testing.T) {
	err := runVerify([]string{})
	if err == nil {
		t.Error("verify with no args should return error")
	}
}

func TestRunVerifyUnknownType(t *testing.T) {
	err := runVerify([]string{"unknown"})
	if err == nil {
		t.Error("verify unknown type should return error")
	}
}

func TestRunVerifySliceMissingID(t *testing.T) {
	err := runVerify([]string{"slice"})
	if err == nil {
		t.Error("verify slice without --id should return error")
	}
}

func TestRunVerifyCellMissingID(t *testing.T) {
	err := runVerify([]string{"cell"})
	if err == nil {
		t.Error("verify cell without --id should return error")
	}
}

func TestRunVerifyJourneyMissingID(t *testing.T) {
	err := runVerify([]string{"journey"})
	if err == nil {
		t.Error("verify journey without --id should return error")
	}
}

func TestRunVerifyTargetsMissingFiles(t *testing.T) {
	err := runVerify([]string{"targets"})
	if err == nil {
		t.Error("verify targets without --files should return error")
	}
}

func TestRunVerifyTargets(t *testing.T) {
	// Provide a file path; the result depends on project metadata.
	err := runVerify([]string{"targets", "--files=cells/access-core/cell.yaml"})
	if err != nil {
		t.Errorf("verify targets should succeed, got: %v", err)
	}
}

func TestRunVerifySliceNotFound(t *testing.T) {
	err := runVerify([]string{"slice", "--id=nonexistent/slice"})
	if err == nil {
		t.Error("verify slice with nonexistent ID should return error")
	}
}

func TestRunVerifyCellNotFound(t *testing.T) {
	err := runVerify([]string{"cell", "--id=nonexistent"})
	if err == nil {
		t.Error("verify cell with nonexistent ID should return error")
	}
}

func TestRunVerifyJourneyNotFound(t *testing.T) {
	err := runVerify([]string{"journey", "--id=nonexistent"})
	if err == nil {
		t.Error("verify journey with nonexistent ID should return error")
	}
}

func TestPrintTargetList(t *testing.T) {
	// Should not panic with empty or non-empty lists.
	printTargetList("Test", nil)
	printTargetList("Test", []string{})
	printTargetList("Test", []string{"a", "b"})
}

// TestIsWithinRoot / TestEvalExistingPrefix previously lived here as a copy
// of kernel/governance's tests. Now that cmd/gocell/app delegates to the
// exported governance.IsWithinRoot / EvalExistingPrefix, coverage lives in
// kernel/governance/validate_test.go — no duplication here.

func TestPrintResult(t *testing.T) {
	// Should not panic.
	printResult(governance.ValidationResult{
		Code:    "TEST-01",
		File:    "test.yaml",
		Field:   "field",
		Message: "msg",
	})
	// Without field.
	printResult(governance.ValidationResult{
		Code:    "TEST-02",
		File:    "test.yaml",
		Message: "msg",
	})
	// Without file.
	printResult(governance.ValidationResult{
		Code:    "TEST-03",
		Message: "msg",
	})
}

// TestPrintResult_IncludesLineColumn verifies the printed output carries the
// field on the message line and a bare file:line:col on the "at" line — this
// keeps the jump target clean for IDE click-to-open handlers.
func TestPrintResult_IncludesLineColumn(t *testing.T) {
	r := governance.ValidationResult{
		Code:    "TEST-10",
		File:    "cells/x/cell.yaml",
		Field:   "id",
		Line:    12,
		Column:  5,
		Message: "boom",
	}
	out := captureStdout(t, func() { printResult(r) })
	// Field moved to the message line.
	if !strings.Contains(out, "boom (field: id)") {
		t.Errorf("printResult output missing 'boom (field: id)' on message line: %q", out)
	}
	// The "at" line carries only file:line:col — no trailing "-> field".
	if !strings.Contains(out, "at cells/x/cell.yaml:12:5\n") {
		t.Errorf("printResult output missing bare 'at file:line:col' line: %q", out)
	}
	if strings.Contains(out, "-> id") {
		t.Errorf("'at' line should not contain '-> field' any more: %q", out)
	}
}

// TestPrintResult_OmitsPositionWhenUnknown: when Line==0 the "at" line shows
// just the file path; the field still lives on the message line.
func TestPrintResult_OmitsPositionWhenUnknown(t *testing.T) {
	r := governance.ValidationResult{
		Code:    "TEST-11",
		File:    "cells/x/cell.yaml",
		Field:   "owner.team",
		Message: "missing",
	}
	out := captureStdout(t, func() { printResult(r) })
	if strings.Contains(out, "cells/x/cell.yaml:") {
		t.Errorf("unexpected position in output %q", out)
	}
	if !strings.Contains(out, "missing (field: owner.team)") {
		t.Errorf("field should appear on message line: %q", out)
	}
	if !strings.Contains(out, "at cells/x/cell.yaml\n") {
		t.Errorf("'at' line should be bare file path: %q", out)
	}
}

// TestPrintResult_LineOnlyColumnZero: Column==0 should not produce a trailing
// ":0"; a bare ":line" is acceptable.
func TestPrintResult_LineOnlyColumnZero(t *testing.T) {
	r := governance.ValidationResult{
		Code: "TEST-12", File: "f.yaml", Line: 7,
		Message: "x",
	}
	out := captureStdout(t, func() { printResult(r) })
	if !strings.Contains(out, "f.yaml:7") {
		t.Errorf("expected f.yaml:7 in %q", out)
	}
	if strings.Contains(out, "f.yaml:7:0") {
		t.Errorf("unexpected trailing :0 in %q", out)
	}
}

// TestPrintResult_Scope: findings anchored to a virtual scope (e.g. DEP-02
// cycle across cells) must render as "[scope: ...]" rather than mimicking
// file:line:col syntax.
func TestPrintResult_Scope(t *testing.T) {
	r := governance.ValidationResult{
		Code:    "DEP-02",
		Scope:   "project",
		Field:   "cells",
		Message: "circular dependency detected",
	}
	out := captureStdout(t, func() { printResult(r) })
	if !strings.Contains(out, "at [scope: project]") {
		t.Errorf("scoped finding should render with '[scope: ...]' marker: %q", out)
	}
	// Defensive: the output must not look like a clickable path.
	if strings.Contains(out, "at project:") || strings.Contains(out, "at project\n") {
		t.Errorf("scope label must not be rendered as a file path: %q", out)
	}
	if !strings.Contains(out, "circular dependency detected (field: cells)") {
		t.Errorf("field should appear on message line for scoped findings too: %q", out)
	}
}

// TestPrintResult_NoFileNoScope: when neither File nor Scope is set, the
// "at" line is omitted entirely (degenerate but legal input).
func TestPrintResult_NoFileNoScope(t *testing.T) {
	r := governance.ValidationResult{
		Code:    "TEST-13",
		Message: "bare",
	}
	out := captureStdout(t, func() { printResult(r) })
	if strings.Contains(out, "at ") {
		t.Errorf("no 'at' line expected when File and Scope both empty: %q", out)
	}
	if !strings.Contains(out, "[TEST-13] bare") {
		t.Errorf("message still expected: %q", out)
	}
}

// captureStdout runs fn with os.Stdout redirected into a string.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	_ = w.Close()
	<-done
	return buf.String()
}
