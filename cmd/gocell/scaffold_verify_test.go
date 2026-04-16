package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/kernel/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunScaffoldCell_Success(t *testing.T) {
	dir := t.TempDir()
	// Create minimal project structure for scaffolder.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cells"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n"), 0o644))

	// Save and restore cwd.
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	err := runScaffold([]string{"cell", "--id=test-cell", "--team=squad"})
	require.NoError(t, err)

	// Verify cell.yaml was created.
	_, statErr := os.Stat(filepath.Join(dir, "cells", "test-cell", "cell.yaml"))
	assert.NoError(t, statErr)
}

func TestRunScaffoldSlice_Success(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cells", "test-cell", "slices"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "test-cell", "cell.yaml"),
		[]byte("id: test-cell\ntype: core\n"), 0o644))

	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	err := runScaffold([]string{"slice", "--id=my-slice", "--cell=test-cell"})
	require.NoError(t, err)
}

func TestRunScaffoldContract_Success(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "contracts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n"), 0o644))

	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	err := runScaffold([]string{"contract", "--id=http.test.v1", "--kind=http", "--owner=test-cell"})
	require.NoError(t, err)
}

func TestRunScaffoldJourney_Success(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "journeys"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n"), 0o644))

	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))

	err := runScaffold([]string{"journey", "--id=J-test", "--goal=test goal", "--team=squad", "--cells=a,b"})
	require.NoError(t, err)
}

func TestPrintVerifyResult_Passed(t *testing.T) {
	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	result := &verify.VerifyResult{
		TargetID: "test-target",
		Passed:   true,
		Results: []verify.TestResult{
			{Name: "unit-test", Passed: true, Output: "all passed"},
		},
	}
	printVerifyResult(result)

	w.Close()
	os.Stdout = old

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	assert.Contains(t, output, "PASSED")
	assert.Contains(t, output, "test-target")
}

func TestPrintVerifyResult_Failed(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	result := &verify.VerifyResult{
		TargetID: "fail-target",
		Passed:   false,
		Results: []verify.TestResult{
			{Name: "unit-test", Passed: false, Output: "error output\nsecond line"},
		},
		Errors:        []error{assert.AnError},
		ManualPending: []string{"manual-check-1"},
	}
	printVerifyResult(result)

	w.Close()
	os.Stdout = old

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	assert.Contains(t, output, "FAILED")
	assert.Contains(t, output, "fail-target")
	assert.Contains(t, output, "error output")
	assert.Contains(t, output, "PENDING")
}

func TestRunGenerateAssembly_MissingID(t *testing.T) {
	err := runGenerate([]string{"assembly"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id is required")
}

func TestRunGenerateAssembly_WithModule(t *testing.T) {
	// generateAssembly with a valid --id and --module exercises the full code
	// path up to the point where project metadata or assembly lookup fails.
	err := runGenerate([]string{"assembly", "--id=test", "--module=example.com/test"})
	require.Error(t, err)
	assert.Regexp(t, `metadata parse|project root|cannot find|generate entrypoint|assembly`,
		err.Error(), "error should originate from the generate-assembly pipeline")
}

func TestRunVerifySlice_ValidID(t *testing.T) {
	err := runVerify([]string{"slice", "--id=access-core/identitymanage"})
	// verifySlice either passes or returns a verify error — never panics.
	if err != nil {
		assert.Contains(t, err.Error(), "verify slice",
			"error should come from the verify pipeline, not a crash")
	}
}

func TestRunVerifyCell_ValidID(t *testing.T) {
	err := runVerify([]string{"cell", "--id=access-core"})
	if err != nil {
		assert.Contains(t, err.Error(), "verify cell",
			"error should come from the verify pipeline, not a crash")
	}
}

func TestRunVerifyJourney_ValidID(t *testing.T) {
	err := runVerify([]string{"journey", "--id=J-user-onboarding"})
	if err != nil {
		assert.Contains(t, err.Error(), "verify journey",
			"error should come from the verify pipeline, not a crash")
	}
}

func TestReadModule_ValidGoMod(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module test.example.com/proj\n\ngo 1.22\n"), 0o644))

	mod, err := readModule(dir)
	require.NoError(t, err)
	assert.Equal(t, "test.example.com/proj", mod)
}

func TestReadModule_MalformedGoMod(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("not a valid go.mod\n"), 0o644))

	_, err := readModule(dir)
	assert.Error(t, err)
}
