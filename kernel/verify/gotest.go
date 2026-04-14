package verify

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// goTestResult holds the outcome of a single `go test` invocation.
type goTestResult struct {
	Output    string // combined stdout+stderr
	Passed    bool   // exit code == 0
	ZeroMatch bool   // go test ran but no tests matched -run pattern
	Err       error  // non-ExitError (command couldn't run at all)
}

// runGoTest executes `go test` with the given arguments in dir.
// It detects zero-match scenarios by scanning output for the well-known
// Go test messages "no tests to run" and "[no test files]".
func runGoTest(ctx context.Context, dir string, args []string) goTestResult {
	fullArgs := append([]string{"test"}, args...)
	cmd := exec.CommandContext(ctx, "go", fullArgs...)
	cmd.Dir = filepath.Clean(dir)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()
	output := buf.String()

	if runErr == nil {
		zm := isZeroMatch(output)
		return goTestResult{Output: output, Passed: true, ZeroMatch: zm}
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return goTestResult{Output: output, Passed: false}
	}

	return goTestResult{
		Output: output,
		Err:    errcode.Wrap(errcode.ErrTestExecution, "go test execution failed", runErr),
	}
}

// isZeroMatch returns true only when NO tests actually ran across all packages.
// Wildcard runs (./...) naturally emit "[no test files]" for empty packages;
// we only flag zero-match if there's no evidence of any test execution.
func isZeroMatch(output string) bool {
	hasNoTests := strings.Contains(output, "no tests to run") ||
		strings.Contains(output, "[no test files]")
	if !hasNoTests {
		return false
	}
	// If any test actually ran, it's not a zero match.
	return !strings.Contains(output, "--- PASS") &&
		!strings.Contains(output, "--- FAIL")
}
