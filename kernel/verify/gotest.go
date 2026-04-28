package verify

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// goTestResult holds the outcome of a single `go test` invocation.
type goTestResult struct {
	Output      string // combined stdout+stderr
	Passed      bool   // exit code == 0
	ZeroMatch   bool   // go test ran but no tests matched -run pattern
	SkippedOnly bool   // go test matched tests, but every matched test skipped
	Err         error  // non-ExitError (command couldn't run at all)
}

type goTestRunner struct {
	goTool string
}

func newGoTestRunner() (goTestRunner, error) {
	path, err := goToolPath()
	if err != nil {
		return goTestRunner{}, errcode.Wrap(errcode.ErrTestExecution, "resolve go tool", err)
	}
	return goTestRunner{goTool: path}, nil
}

// runGoTest executes `go test` with the given arguments in dir.
// It detects zero-match scenarios by scanning output for the well-known
// Go test messages "no tests to run" and "[no test files]".
func runGoTest(ctx context.Context, dir string, args []string) goTestResult {
	runner, err := newGoTestRunner()
	if err != nil {
		return goTestResult{Err: err}
	}
	return runner.run(ctx, dir, args)
}

func (r goTestRunner) run(ctx context.Context, dir string, args []string) goTestResult {
	fullArgs := append([]string{"test"}, args...)
	cmd := exec.CommandContext(ctx, r.goTool, fullArgs...)
	cmd.Dir = filepath.Clean(dir)
	cmd.Env = goTestEnv(r.goTool)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()
	output := buf.String()

	if runErr == nil {
		zm := isZeroMatch(output)
		return goTestResult{Output: output, Passed: true, ZeroMatch: zm, SkippedOnly: !zm && isSkipOnly(output)}
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

func goToolPath() (string, error) {
	path, err := exec.LookPath(goToolName())
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Abs(path)
}

func goToolName() string {
	if runtime.GOOS == "windows" {
		return "go.exe"
	}
	return "go"
}

func goTestEnv(goTool string) []string {
	env := os.Environ()
	pathKey, pathValue := pathEnv(env)
	env = withoutEnvKeys(env, "PATH", "Path")
	return append(env, pathKey+"="+prependPath(filepath.Dir(goTool), pathValue))
}

func withoutEnvKeys(env []string, keys ...string) []string {
	filtered := env[:0]
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !matchesAnyEnvKey(key, keys) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func matchesAnyEnvKey(key string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(key, candidate) {
			return true
		}
	}
	return false
}

func pathEnv(env []string) (string, string) {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "PATH") {
			return key, value
		}
	}
	return "PATH", ""
}

func prependPath(dir, pathValue string) string {
	if dir == "" {
		return pathValue
	}
	if pathValue == "" {
		return dir
	}
	return dir + string(os.PathListSeparator) + pathValue
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
	if strings.Contains(output, "--- SKIP") {
		return false
	}
	// If any test actually ran, it's not a zero match.
	return !strings.Contains(output, "--- PASS") &&
		!strings.Contains(output, "--- FAIL")
}

func isSkipOnly(output string) bool {
	return strings.Contains(output, "--- SKIP") &&
		!strings.Contains(output, "--- PASS") &&
		!strings.Contains(output, "--- FAIL")
}
