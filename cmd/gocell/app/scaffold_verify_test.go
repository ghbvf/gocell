package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/cmd/gocell/app/printers"
	"github.com/ghbvf/gocell/kernel/verify"
)

// These happy-path tests now go through runScaffoldWithRoot so they can run
// against t.TempDir without needing os.Chdir — see F-SEC-03 in review PR#164.

func TestRunScaffoldCell_Success(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cells"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))

	err := runScaffoldWithRoot(context.Background(), dir,
		[]string{"cell", "--id=testcell", "--team=squad", "--role=cell-owner"})
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(dir, "cells", "testcell", "cell.yaml"))
	assert.NoError(t, statErr)
}

func TestRunScaffoldSlice_Success(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cells", "testcell", "slices"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "testcell", "cell.yaml"),
		[]byte("id: testcell\ntype: core\n"), 0o644))

	err := runScaffoldWithRoot(context.Background(), dir,
		[]string{"slice", "--id=myslice", "--cell=testcell"})
	require.NoError(t, err)
}

// TestRunScaffoldSlice_GeneratesParseableSliceYaml asserts that the scaffold
// output is parseable by the strict metadata parser (includes consistencyLevel).
func TestRunScaffoldSlice_GeneratesParseableSliceYaml(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		wantLevel string
	}{
		{
			name:      "default level L1",
			args:      []string{"slice", "--id=myslice", "--cell=testcell"},
			wantLevel: "L1",
		},
		{
			name:      "explicit level L0",
			args:      []string{"slice", "--id=myslice", "--cell=testcell", "--level=L0"},
			wantLevel: "L0",
		},
		{
			name:      "explicit level L2",
			args:      []string{"slice", "--id=myslice", "--cell=testcell", "--level=L2"},
			wantLevel: "L2",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "cells", "testcell", "slices"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
				[]byte("module example.com/test\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "testcell", "cell.yaml"),
				[]byte("id: testcell\ntype: core\n"), 0o644))

			err := runScaffoldWithRoot(context.Background(), dir, tt.args)
			require.NoError(t, err)

			sliceYAML := filepath.Join(dir, "cells", "testcell", "slices", "myslice", "slice.yaml")
			raw, err := os.ReadFile(sliceYAML) //nolint:gosec // tempdir test fixture
			require.NoError(t, err)

			// Parse with strict YAML parser — must not error.
			var m map[string]any
			require.NoError(t, strictUnmarshalYAML(raw, &m), "scaffold output must be parseable by strict YAML parser")
			assert.Equal(t, "myslice", m["id"], "id field mismatch")
			assert.Equal(t, "testcell", m["belongsToCell"], "belongsToCell field mismatch")
			assert.Equal(t, tt.wantLevel, m["consistencyLevel"], "consistencyLevel field must match --level")
		})
	}
}

// TestRunScaffoldSlice_InvalidLevel asserts that --level rejects non-LN values.
func TestRunScaffoldSlice_InvalidLevel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cells", "testcell", "slices"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cells", "testcell", "cell.yaml"),
		[]byte("id: testcell\ntype: core\n"), 0o644))

	err := runScaffoldWithRoot(context.Background(), dir,
		[]string{"slice", "--id=myslice", "--cell=testcell", "--level=invalid"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_SCAFFOLD_INVALID_OPTS")
}

func TestRunScaffoldContract_Success(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "contracts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))

	err := runScaffoldWithRoot(context.Background(), dir,
		[]string{"contract", "--id=http.test.v1", "--kind=http", "--owner=testcell"})
	require.NoError(t, err)
}

func TestRunScaffoldJourney_Success(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "journeys"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n"), 0o644))

	err := runScaffoldWithRoot(context.Background(), dir,
		[]string{"journey", "--id=J-test", "--goal=test goal", "--team=squad", "--cells=a,b"})
	require.NoError(t, err)
}

func TestVerifyTextPrinter_Passed(t *testing.T) {
	result := &verify.VerifyResult{
		TargetID: "test-target",
		Passed:   true,
		Results: []verify.TestResult{
			{Name: "unit-test", Passed: true, Output: "all passed"},
		},
	}

	var buf bytes.Buffer
	p, err := printers.NewVerifyPrinter("text", &buf)
	require.NoError(t, err)
	require.NoError(t, p.Print(result))
	output := buf.String()

	assert.Contains(t, output, "PASSED")
	assert.Contains(t, output, "test-target")
}

func TestVerifyTextPrinter_Failed(t *testing.T) {
	result := &verify.VerifyResult{
		TargetID: "fail-target",
		Passed:   false,
		Results: []verify.TestResult{
			{Name: "unit-test", Passed: false, Output: "error output\nsecond line"},
		},
		Errors:        []error{assert.AnError},
		ManualPending: []string{"manual-check-1"},
	}

	var buf bytes.Buffer
	p, err := printers.NewVerifyPrinter("text", &buf)
	require.NoError(t, err)
	require.NoError(t, p.Print(result))
	output := buf.String()

	assert.Contains(t, output, "FAILED")
	assert.Contains(t, output, "fail-target")
	assert.Contains(t, output, "error output")
	assert.Contains(t, output, "PENDING")
}

func TestVerifyJSONPrinter_Schema(t *testing.T) {
	result := &verify.VerifyResult{
		TargetID: "accesscore/sessions",
		Passed:   true,
		Results: []verify.TestResult{
			{Name: "TestLogin", Passed: true, Output: "ok", ZeroMatch: false},
		},
		Errors:        []error{assert.AnError},
		ManualPending: []string{"check-1"},
	}

	var buf bytes.Buffer
	p, err := printers.NewVerifyPrinter("json", &buf)
	require.NoError(t, err)
	require.NoError(t, p.Print(result))

	// Re-parse into a local DTO that mirrors the wire schema.
	type testResultDTO struct {
		Name        string `json:"name"`
		Passed      bool   `json:"passed"`
		Output      string `json:"output"`
		ZeroMatch   bool   `json:"zeroMatch"`
		SkippedOnly bool   `json:"skippedOnly"`
	}
	type errorDetailDTO struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	}
	type errorDTO struct {
		Code    string           `json:"code"`
		Message string           `json:"message"`
		Details []errorDetailDTO `json:"details"`
	}
	type doc struct {
		TargetID      string          `json:"targetId"`
		Passed        bool            `json:"passed"`
		Results       []testResultDTO `json:"results"`
		Errors        []errorDTO      `json:"errors"`
		ManualPending []string        `json:"manualPending"`
	}

	var got doc
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "accesscore/sessions", got.TargetID)
	assert.True(t, got.Passed)
	require.Len(t, got.Results, 1)
	assert.Equal(t, "TestLogin", got.Results[0].Name)
	assert.True(t, got.Results[0].Passed)
	assert.Equal(t, "ok", got.Results[0].Output)
	assert.False(t, got.Results[0].ZeroMatch)
	assert.False(t, got.Results[0].SkippedOnly)
	require.Len(t, got.Errors, 1)
	assert.Equal(t, "ERR_INTERNAL", got.Errors[0].Code)
	assert.NotEmpty(t, got.Errors[0].Message)
	assert.Empty(t, got.Errors[0].Details)
	require.Len(t, got.ManualPending, 1)
	assert.Equal(t, "check-1", got.ManualPending[0])
}

func TestVerifyJSONPrinter_NilSlicesEmitEmptyArrays(t *testing.T) {
	result := &verify.VerifyResult{
		TargetID: "x",
		Passed:   true,
		// Results, Errors, ManualPending intentionally nil
	}

	var buf bytes.Buffer
	p, err := printers.NewVerifyPrinter("json", &buf)
	require.NoError(t, err)
	require.NoError(t, p.Print(result))

	body := buf.String()
	assert.Contains(t, body, `"results": []`, "nil Results must serialize as []")
	assert.Contains(t, body, `"errors": []`, "nil Errors must serialize as []")
	assert.Contains(t, body, `"manualPending": []`, "nil ManualPending must serialize as []")
	assert.NotContains(t, body, "null", "no field should be null")
}

func TestNewVerifyPrinter_RejectsSARIF(t *testing.T) {
	var buf bytes.Buffer
	_, err := printers.NewVerifyPrinter("sarif", &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SARIF not supported")
	assert.Contains(t, err.Error(), "test-execution outcomes",
		"error must explain why SARIF is rejected so callers understand the constraint")
}

func TestNewVerifyPrinter_RejectsUnknown(t *testing.T) {
	var buf bytes.Buffer
	_, err := printers.NewVerifyPrinter("yaml", &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported format")
}

func TestRunGenerateAssembly_MissingID(t *testing.T) {
	err := runGenerate(context.Background(), []string{"assembly"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage: gocell generate assembly")
}

func TestRunGenerateAssembly_WithModule(t *testing.T) {
	// generateAssembly with a valid --id and --module exercises the full code
	// path up to the point where project metadata or assembly lookup fails.
	err := runGenerate(context.Background(), []string{"assembly", "--id=test", "--module=example.com/test"})
	require.Error(t, err)
	assert.Regexp(t, `metadata parse|project root|cannot find|generate entrypoint|assembly`,
		err.Error(), "error should originate from the generate-assembly pipeline")
}

func TestRunVerifySlice_ValidID(t *testing.T) {
	err := runVerify(context.Background(), []string{"slice", "--id=accesscore/identitymanage"})
	// verifySlice either passes or returns a verify error — never panics.
	if err != nil {
		assert.Contains(t, err.Error(), "verify slice",
			"error should come from the verify pipeline, not a crash")
	}
}

func TestRunVerifyCell_ValidID(t *testing.T) {
	err := runVerify(context.Background(), []string{"cell", "--id=accesscore"})
	if err != nil {
		assert.Contains(t, err.Error(), "verify cell",
			"error should come from the verify pipeline, not a crash")
	}
}

func TestRunVerifyJourney_ValidID(t *testing.T) {
	err := runVerify(context.Background(), []string{"journey", "--id=J-useronboarding"})
	if err != nil {
		assert.Contains(t, err.Error(), "verify journey",
			"error should come from the verify pipeline, not a crash")
	}
}

// TestRunVerify_RejectsSARIFBeforeExecution locks the contract that an
// unsupported --format is caught up front — before metadata parse or any
// test execution. CI invocations that misconfigure the format flag should
// fail in milliseconds, not after running the full verify suite. The
// assertion targets the SARIF rejection wording from NewVerifyPrinter, so
// any future regression that delays format validation past the runner
// call will produce a different (verify-pipeline) error and trip this test.
func TestRunVerify_RejectsSARIFBeforeExecution(t *testing.T) {
	err := runVerify(context.Background(), []string{"slice", "--id=accesscore/identitymanage", "--format=sarif"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SARIF not supported",
		"format must be rejected before runner execution")
	assert.NotContains(t, err.Error(), "verify slice:",
		"runner pipeline must not be touched when format is unsupported")
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

// strictUnmarshalYAML unmarshals raw YAML into dest using KnownFields=true, so
// unknown keys cause an error. Used by scaffold slice tests to verify the
// generated slice.yaml is parseable.
func strictUnmarshalYAML(raw []byte, dest any) error {
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	return dec.Decode(dest)
}
