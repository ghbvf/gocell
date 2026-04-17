package app

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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

// TestDispatch_Contract pins the exit-code and stream-split behaviour of the
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
			name:      "no args prints usage to stdout and returns 1",
			args:      []string{},
			wantExit:  1,
			stdoutSub: []string{"Usage: gocell", "validate", "scaffold"},
			// Usage itself is not an error condition, so nothing on stderr.
		},
		{
			name:      "unknown command goes to stderr, usage to stdout, returns 1",
			args:      []string{"bogus-command"},
			wantExit:  1,
			stdoutSub: []string{"Usage: gocell"},
			stderrSub: []string{"unknown command: bogus-command"},
		},
		{
			name:         "sub-command error goes to stderr, NOT stdout, returns 1",
			args:         []string{"scaffold"}, // missing sub-kind → runScaffold returns error
			wantExit:     1,
			stderrSub:    []string{"error:", "usage: gocell scaffold"},
			stdoutNotSub: []string{"error:"},
		},
		{
			name:     "scaffold unknown kind returns 1 via sub-command error path",
			args:     []string{"scaffold", "not-a-kind"},
			wantExit: 1,
			stderrSub: []string{
				"error:",
				"unknown scaffold type",
			},
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
	assert.Equal(t, 0, exit, "stderr=%q", stderr)
	assert.Empty(t, strings.TrimSpace(stderr), "nothing on stderr for success")
	assert.Contains(t, stdout, "Validation complete:", "summary line expected")
}
