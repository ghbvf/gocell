package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureDispatch runs Dispatch with args while capturing stdout and stderr
// separately, so contract tests can assert which stream each message lands on.
func captureDispatch(t *testing.T, args []string) (exit int, stdout, stderr string) {
	t.Helper()

	origOut, origErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()

	doneOut, doneErr := make(chan struct{}), make(chan struct{})
	var bufOut, bufErr bytes.Buffer
	go func() { _, _ = io.Copy(&bufOut, rOut); close(doneOut) }()
	go func() { _, _ = io.Copy(&bufErr, rErr); close(doneErr) }()

	exit = Dispatch(args)
	_ = wOut.Close()
	_ = wErr.Close()
	<-doneOut
	<-doneErr
	return exit, bufOut.String(), bufErr.String()
}

// TestDispatch_Contract pins the exit-code and stream-split behavior of the
// top-level entry point. Regressions here would otherwise only surface through
// shell-level flake after merge; we want a fast CI guard. Kept table-driven
// so new edge cases (e.g. `--help`) can be added without new test fns.
func TestDispatch_Contract(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantExit     int
		stdoutSub    []string // substrings expected on stdout
		stderrSub    []string // substrings expected on stderr
		stdoutNotSub []string // substrings that must NOT appear on stdout
	}{
		{
			name:      "no args prints usage to stdout and returns ExitUsage",
			args:      []string{},
			wantExit:  ExitUsage,
			stdoutSub: []string{"Usage: gocell", "validate", "scaffold"},
			// Usage itself is not an error condition, so nothing on stderr.
		},
		{
			name:      "unknown command returns ExitUsage (distinct from runtime error)",
			args:      []string{"bogus-command"},
			wantExit:  ExitUsage,
			stdoutSub: []string{"Usage: gocell"},
			stderrSub: []string{"unknown command: bogus-command"},
		},
		{
			name:         "sub-command error returns ExitRuntime, stderr carries error",
			args:         []string{"scaffold"}, // missing sub-kind → runScaffold returns error
			wantExit:     ExitRuntime,
			stderrSub:    []string{"error:", "usage: gocell scaffold"},
			stdoutNotSub: []string{"error:"},
		},
		{
			name:     "scaffold unknown kind returns ExitRuntime via sub-command error path",
			args:     []string{"scaffold", "not-a-kind"},
			wantExit: ExitRuntime,
			stderrSub: []string{
				"error:",
				"unknown scaffold type",
			},
		},
		{
			// flag.ErrHelp must propagate through Dispatch as a successful
			// help invocation, not a runtime error. Regression guard: the
			// previous io.Discard + fmt.Errorf wrapping turned `-h` into
			// "error: graph: flag: help requested" with exit 1.
			//
			// stdlib flag emits single-dash flag names in usage; we assert
			// "-format" / "-root" without expanding the test to long form.
			name:         "graph -h is a successful help request, exit ExitOK",
			args:         []string{"graph", "-h"},
			wantExit:     ExitOK,
			stderrSub:    []string{"Usage of graph", "-format", "-root", "-include-tests"},
			stdoutNotSub: []string{"error:"},
		},
		{
			// Same contract for the long form. flag package reports both
			// `-h` and `-help` as ErrHelp.
			name:      "graph --help is a successful help request, exit ExitOK",
			args:      []string{"graph", "--help"},
			wantExit:  ExitOK,
			stderrSub: []string{"Usage of graph"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exit, stdout, stderr := captureDispatch(t, tt.args)
			assert.Equal(t, tt.wantExit, exit,
				"exit code mismatch\nstdout=%q\nstderr=%q", stdout, stderr)
			for _, s := range tt.stdoutSub {
				assert.Contains(t, stdout, s, "expected %q on stdout", s)
			}
			for _, s := range tt.stderrSub {
				assert.Contains(t, stderr, s, "expected %q on stderr", s)
			}
			for _, s := range tt.stdoutNotSub {
				assert.NotContains(t, stdout, s, "unexpected %q on stdout", s)
			}
		})
	}
}

// TestDispatch_SuccessPath_ExitZero ensures the success branch (exit 0,
// nothing on stderr) is also pinned. We drive it through `validate` against
// a temp project with no metadata — it produces a clean pass and returns 0.
func TestDispatch_SuccessPath_ExitZero(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/go.mod",
		[]byte("module example.com/empty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exit, stdout, stderr := captureDispatch(t, []string{"validate", "--root", dir})
	assert.Equal(t, ExitOK, exit, "stderr=%q", stderr)
	assert.Empty(t, strings.TrimSpace(stderr), "nothing on stderr for success")
	assert.Contains(t, stdout, "Validation complete:", "summary line expected")
}

// TestDispatch_ValidateFormats pins the contract for `gocell validate
// --format=...` at the integration boundary. Plan Batch C #12: machine-
// parseable formats must produce well-formed output and the unknown-format
// path must surface a recognizable error. Three cases × empty project so
// the validator emits a clean pass; we then assert format-specific shape.
func TestDispatch_ValidateFormats(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/go.mod",
		[]byte("module example.com/empty\n"), 0o644))

	t.Run("json on clean project", func(t *testing.T) {
		exit, stdout, stderr := captureDispatch(t,
			[]string{"validate", "--root", dir, "--format=json"})
		assert.Equal(t, ExitOK, exit, "stderr=%q", stderr)
		assert.Empty(t, strings.TrimSpace(stderr))

		var doc struct {
			Issues  []map[string]any `json:"issues"`
			Summary struct {
				Errors   int `json:"errors"`
				Warnings int `json:"warnings"`
			} `json:"summary"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdout), &doc),
			"stdout must be valid JSON: %q", stdout)
		assert.NotNil(t, doc.Issues,
			"issues key must be present (never null) even on clean runs")
	})

	t.Run("sarif on clean project", func(t *testing.T) {
		exit, stdout, stderr := captureDispatch(t,
			[]string{"validate", "--root", dir, "--format=sarif"})
		assert.Equal(t, ExitOK, exit, "stderr=%q", stderr)

		var sarifLog struct {
			Schema  string `json:"$schema"`
			Version string `json:"version"`
			Runs    []struct {
				Tool struct {
					Driver struct {
						Name string `json:"name"`
					} `json:"driver"`
				} `json:"tool"`
				Results []map[string]any `json:"results"`
			} `json:"runs"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdout), &sarifLog),
			"stdout must be valid SARIF JSON: %q", stdout)
		assert.Equal(t, "2.1.0", sarifLog.Version)
		require.Len(t, sarifLog.Runs, 1)
		assert.Equal(t, "gocell", sarifLog.Runs[0].Tool.Driver.Name)
	})

	t.Run("unknown format returns ExitRuntime with descriptive stderr", func(t *testing.T) {
		exit, _, stderr := captureDispatch(t,
			[]string{"validate", "--root", dir, "--format=yaml"})
		// Unknown format surfaces from runValidate -> printers.New, so the
		// dispatcher categorizes it as a sub-command runtime error
		// (ExitRuntime), not an unknown-command usage error (ExitUsage).
		// This is consistent with how scaffold/check report sub-flag
		// errors today.
		assert.Equal(t, ExitRuntime, exit)
		assert.Contains(t, stderr, "unknown format")
		assert.Contains(t, stderr, "supported formats are",
			"error must list valid formats so users self-correct")
	})
}
