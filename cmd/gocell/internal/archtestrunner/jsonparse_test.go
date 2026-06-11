package archtestrunner

import (
	"fmt"
	"os/exec"
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
	results := parseTestJSON([]byte(cannedJSONOutput()))

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
	results := parseTestJSON([]byte(output))
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
	results := parseTestJSON([]byte(buildFailOutput))
	// No test events at all — results should be empty or only have package-level events.
	// The infra-error path is triggered by the runner, not parser.
	// Parser just returns what events it can parse.
	for _, r := range results {
		assert.NotEqual(t, "", r.Name, "should not have empty-name test results from build noise")
	}
}

func TestDeterminePassFail_ExitErrorIsTestFailure(t *testing.T) {
	// An *exec.ExitError means tests ran but some failed — not an infra error.
	exitErr := &exec.ExitError{}
	infra := determineInfraErr(exitErr)
	assert.Nil(t, infra, "ExitError should not be an infra error")
}

func TestDeterminePassFail_OtherErrorIsInfra(t *testing.T) {
	// A non-ExitError (e.g. command not found) is an infra error.
	type customErr struct{ s string }
	// Implement the error interface.
	toErr := func(e *customErr) error {
		return fmt.Errorf("custom: %s", e.s)
	}
	infra := determineInfraErr(toErr(&customErr{"no such file"}))
	assert.NotNil(t, infra, "non-ExitError should be an infra error")
}

func TestDeterminePassFail_NilIsNotInfra(t *testing.T) {
	infra := determineInfraErr(nil)
	assert.Nil(t, infra)
}

func TestWriteJSONOut_FilterValidLines(t *testing.T) {
	// Verify that writeJSONOut only writes lines that are valid JSON event lines
	// (have "Action" field), discarding build noise.
	mixed := `go: downloading module
{"Action":"run","Test":"TestX"}
not-json
{"Action":"pass","Test":"TestX","Elapsed":0.1}
`
	out := collectValidJSONLines([]byte(mixed))
	assert.Len(t, out, 2)
	assert.Contains(t, string(out[0]), `"Action"`)
	assert.Contains(t, string(out[1]), `"Action"`)
}

// TestCollectValidJSONLines_NoActionField verifies that a valid JSON object
// without an "Action" field is filtered out (not a genuine test2json event).
func TestCollectValidJSONLines_NoActionField(t *testing.T) {
	input := []byte(`{"Foo":"bar"}` + "\n" + `{"Action":"pass","Test":"TestX","Elapsed":0.1}` + "\n")
	out := collectValidJSONLines(input)
	require.Len(t, out, 1, "JSON object without Action field must be excluded")
	assert.Contains(t, string(out[0]), `"Action":"pass"`)
}
