package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/governance"
)

func TestPrintUsage(t *testing.T) {
	// Should not panic.
	printUsage()
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

	// findRoot should find the go.mod in the src/ directory (or wherever it is
	// relative to the test binary). Walk up from current test dir to find it.
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
	if err != nil {
		t.Errorf("generate indexes should return nil, got: %v", err)
	}
}

func TestRunGenerateBoundaries(t *testing.T) {
	err := runGenerate([]string{"boundaries"})
	if err != nil {
		t.Errorf("generate boundaries should return nil, got: %v", err)
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
		if err != nil {
			t.Errorf("check %s should succeed (placeholder), got: %v", name, err)
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
