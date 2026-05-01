package verify

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ghbvf/gocell/pkg/cmdrun"
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
	goTool cmdrun.ValidatedTool
}

func newGoTestRunner() (goTestRunner, error) {
	tool, err := cmdrun.NewTool(goToolName())
	if err != nil {
		return goTestRunner{}, errcode.Wrap(errcode.ErrTestExecution, "resolve go tool", err)
	}
	return goTestRunner{goTool: tool}, nil
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
	output, runErr := cmdrun.RunIn(ctx, r.goTool, filepath.Clean(dir), goTestEnv(r.goTool.Dir()), fullArgs...)
	out := string(output)

	if runErr == nil {
		zm := isZeroMatch(out)
		return goTestResult{Output: out, Passed: true, ZeroMatch: zm, SkippedOnly: !zm && isSkipOnly(out)}
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return goTestResult{Output: out, Passed: false}
	}

	return goTestResult{
		Output: out,
		Err:    errcode.Wrap(errcode.ErrTestExecution, "go test execution failed", runErr),
	}
}

func goToolName() string {
	if runtime.GOOS == "windows" {
		return "go.exe"
	}
	return "go"
}

// goTestEnv returns os.Environ with PATH rewritten to put goToolDir first,
// so go-toolchain-internal helpers (compile, link, vet) resolve to the
// toolchain that owns the go binary rather than any other version that
// might happen to be earlier on PATH.
func goTestEnv(goToolDir string) []string {
	env := os.Environ()
	pathKey, pathValue := pathEnv(env)
	env = withoutPathEnv(env)
	return append(env, pathKey+"="+prependPath(goToolDir, pathValue))
}

func withoutPathEnv(env []string) []string {
	filtered := env[:0]
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !matchesPathEnvKey(key) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func matchesPathEnvKey(key string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(key, "PATH")
	}
	return key == "PATH"
}

func pathEnv(env []string) (string, string) {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && matchesPathEnvKey(key) {
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
