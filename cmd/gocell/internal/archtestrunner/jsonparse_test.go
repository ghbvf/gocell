package archtestrunner

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cannedJSONLines are typical `go test -json` lines with pass/fail/skip/noise.
// Kept as a slice to avoid raw-string line-length constraints.
var cannedJSONLines = []string{
	`go: downloading some/module v0.0.1`,
	`{"Time":"2026-01-01T00:00:00Z","Action":"run","Package":"./tools/archtest","Test":"TestAlpha"}`,
	`{"Time":"2026-01-01T00:00:01Z","Action":"output","Package":"./tools/archtest",` +
		`"Test":"TestAlpha","Output":"    layer_test.go:42: violation found\n"}`,
	`{"Time":"2026-01-01T00:00:01Z","Action":"fail","Package":"./tools/archtest","Test":"TestAlpha","Elapsed":1.5}`,
	`{"Time":"2026-01-01T00:00:02Z","Action":"run","Package":"./tools/archtest","Test":"TestBeta"}`,
	`{"Time":"2026-01-01T00:00:03Z","Action":"pass","Package":"./tools/archtest","Test":"TestBeta","Elapsed":0.1}`,
	`{"Time":"2026-01-01T00:00:04Z","Action":"run","Package":"./tools/archtest","Test":"TestGamma"}`,
	`{"Time":"2026-01-01T00:00:05Z","Action":"skip","Package":"./tools/archtest","Test":"TestGamma","Elapsed":0.0}`,
	`not-json-noise-line`,
	`{"Time":"2026-01-01T00:00:06Z","Action":"fail","Package":"./tools/archtest","Elapsed":1.6}`,
}

// cannedJSONOutput assembles the canned lines into a single newline-joined string.
func cannedJSONOutput() string {
	result := ""
	for _, line := range cannedJSONLines {
		result += line + "\n"
	}
	return result
}

func TestParseTestJSON_PassFailSkip(t *testing.T) {
	results, err := parseTestJSON([]byte(cannedJSONOutput()))
	require.NoError(t, err)

	// Find by name.
	byName := make(map[string]TestResult)
	for _, r := range results {
		byName[r.Name] = r
	}

	alpha, ok := byName["TestAlpha"]
	require.True(t, ok, "TestAlpha must be in results")
	assert.Equal(t, "fail", alpha.Status)
	assert.InDelta(t, 1.5, alpha.Elapsed, 0.001)
	assert.Contains(t, alpha.Output, "violation found")

	beta, ok := byName["TestBeta"]
	require.True(t, ok, "TestBeta must be in results")
	assert.Equal(t, "pass", beta.Status)
	assert.InDelta(t, 0.1, beta.Elapsed, 0.001)

	gamma, ok := byName["TestGamma"]
	require.True(t, ok, "TestGamma must be in results")
	assert.Equal(t, "skip", gamma.Status)
}

func TestParseTestJSON_SkipsNonJSONLines(t *testing.T) {
	// Ensures that build-noise lines and non-JSON lines don't cause panic or
	// spurious results.
	output := `go: downloading
not valid json at all
{"Action":"run","Test":"TestX"}
{"Action":"pass","Test":"TestX","Elapsed":0.5}
`
	results, err := parseTestJSON([]byte(output))
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "pass", results[0].Status)
	assert.Equal(t, "TestX", results[0].Name)
}

func TestParseTestJSON_BuildFailureSynthetic(t *testing.T) {
	// A build failure produces output lines but no test-level pass/fail/skip.
	buildFailOutput := `# tools/archtest
tools/archtest/broken_test.go:5:2: undefined: SomeMissing
FAIL	tools/archtest [build failed]
`
	results, err := parseTestJSON([]byte(buildFailOutput))
	require.NoError(t, err)
	// No test events at all — results should be empty or only have package-level events.
	// The infra-error path is triggered by the runner, not parser.
	// Parser just returns what events it can parse.
	for _, r := range results {
		assert.NotEqual(t, "", r.Name, "should not have empty-name test results from build noise")
	}
}

// TestScanJSONLines_LongLineSurfacesError is the F2 regression: a single line
// exceeding maxJSONLineBytes must surface as an error, NOT silently truncate
// the parse. Before the fix, the default bufio.Scanner cap (64 KiB) stopped
// scanning and the unchecked scanner.Err() dropped every later event.
func TestScanJSONLines_LongLineSurfacesError(t *testing.T) {
	longLine := strings.Repeat("x", maxJSONLineBytes+1)
	output := []byte(longLine + "\n" + `{"Action":"pass","Test":"TestX","Elapsed":0.1}` + "\n")

	_, err := parseTestJSON(output)
	require.Error(t, err, "an over-cap line must surface, not silently truncate the report")
	assert.Contains(t, err.Error(), "scan go test -json output")

	_, cerr := collectValidJSONLines(output)
	require.Error(t, cerr, "collectValidJSONLines must also surface the scan error, not write a truncated artifact")
}

// TestScanJSONLines_WithinCapPassesThrough confirms the raised buffer admits a
// large-but-bounded line (anti-vacuity for the cap: it does not reject normal
// long archtest diffs, only pathological over-cap ones).
func TestScanJSONLines_WithinCapPassesThrough(t *testing.T) {
	bigButOK := strings.Repeat("y", 1<<20) // 1 MiB << 10 MiB cap
	line := `{"Action":"output","Test":"TestX","Output":"` + bigButOK + `"}`
	output := []byte(line + "\n" + `{"Action":"fail","Test":"TestX","Elapsed":0.1}` + "\n")

	results, err := parseTestJSON(output)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "fail", results[0].Status)
	assert.Contains(t, results[0].Output, bigButOK)
}

func TestClassifyRunError_ExitErrorWithResultsIsTestFailure(t *testing.T) {
	// An *exec.ExitError WITH parsed test results means tests ran and some
	// failed — not an infra error.
	exitErr := &exec.ExitError{}
	tests := []TestResult{{Name: "TestA", Status: "fail"}}
	assert.Nil(t, classifyRunError(exitErr, tests, nil),
		"ExitError with test results should not be an infra error")
}

func TestClassifyRunError_BuildFailSurfacesDiagnostic(t *testing.T) {
	// An *exec.ExitError with ZERO parsed results is a build/package failure:
	// the raw diagnostic must reach the operator, not an empty "0 failing" report.
	exitErr := &exec.ExitError{}
	buildOut := []byte(`# tools/archtest
tools/archtest/broken_test.go:5:2: undefined: SomeMissing
FAIL	tools/archtest [build failed]
`)
	err := classifyRunError(exitErr, nil, buildOut)
	require.Error(t, err, "build failure with no test results must be surfaced")
	assert.Contains(t, err.Error(), "no test results")
	assert.Contains(t, err.Error(), "undefined: SomeMissing",
		"the actual build diagnostic must be carried in the error")
}

func TestClassifyRunError_OtherErrorIsInfra(t *testing.T) {
	// A non-ExitError (e.g. command not found) is an infra error regardless of results.
	infra := classifyRunError(fmt.Errorf("custom: no such file"), nil, nil)
	require.Error(t, infra, "non-ExitError should be an infra error")
	assert.Contains(t, infra.Error(), "go test execution failed")
}

func TestClassifyRunError_NilIsNil(t *testing.T) {
	assert.Nil(t, classifyRunError(nil, nil, nil))
}

func TestBuildFailureDiagnostic_ReconstructsOutput(t *testing.T) {
	// Output fields of all events (including package-level Test=="") plus
	// non-JSON lines are reconstructed into one human-readable diagnostic.
	out := []byte(`# tools/archtest
{"Action":"build-output","ImportPath":"x","Output":"broken_test.go:5: undefined: Foo\n"}
{"Action":"output","Output":"FAIL\ttools/archtest [build failed]\n"}
`)
	diag, err := buildFailureDiagnostic(out)
	require.NoError(t, err)
	assert.Contains(t, diag, "# tools/archtest", "non-JSON build-noise lines pass through")
	assert.Contains(t, diag, "undefined: Foo", "build-output event Output is reconstructed")
	assert.Contains(t, diag, "[build failed]")
}

func TestWriteJSONOut_FilterValidLines(t *testing.T) {
	// Verify that writeJSONOut only writes lines that are valid JSON event lines
	// (have "Action" field), discarding build noise.
	mixed := `go: downloading module
{"Action":"run","Test":"TestX"}
not-json
{"Action":"pass","Test":"TestX","Elapsed":0.1}
`
	out, err := collectValidJSONLines([]byte(mixed))
	require.NoError(t, err)
	assert.Len(t, out, 2)
	assert.Contains(t, string(out[0]), `"Action"`)
	assert.Contains(t, string(out[1]), `"Action"`)
}

// TestCollectValidJSONLines_NoActionField verifies that a valid JSON object
// without an "Action" field is filtered out (not a genuine test2json event).
func TestCollectValidJSONLines_NoActionField(t *testing.T) {
	input := []byte(`{"Foo":"bar"}` + "\n" + `{"Action":"pass","Test":"TestX","Elapsed":0.1}` + "\n")
	out, err := collectValidJSONLines(input)
	require.NoError(t, err)
	require.Len(t, out, 1, "JSON object without Action field must be excluded")
	assert.Contains(t, string(out[0]), `"Action":"pass"`)
}
