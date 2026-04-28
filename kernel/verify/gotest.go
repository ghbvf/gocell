package verify

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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

// runGoTest executes `go test` with the given arguments in dir.
// It detects zero-match scenarios by scanning output for the well-known
// Go test messages "no tests to run" and "[no test files]".
func runGoTest(ctx context.Context, dir string, args []string) goTestResult {
	fullArgs := append([]string{"test"}, args...)
	cmd := exec.CommandContext(ctx, goToolPath(), fullArgs...)
	cmd.Dir = filepath.Clean(dir)
	cmd.Env = goTestEnv()

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

func goToolPath() string {
	name := goToolName()
	for _, dir := range fixedGoToolDirs() {
		candidate := filepath.Join(dir, name)
		if isExecutableFile(candidate) {
			return candidate
		}
	}

	return filepath.Join(fixedGoToolDirs()[0], name)
}

func goToolName() string {
	if runtime.GOOS == "windows" {
		return "go.exe"
	}
	return "go"
}

func goTestEnv() []string {
	env := withoutEnvKeys(os.Environ(), "PATH", "Path", "GOROOT")
	return append(env,
		"PATH="+fixedGoTestPath(),
	)
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

func fixedGoTestPath() string {
	return strings.Join(fixedGoToolDirs(), string(os.PathListSeparator))
}

func fixedGoToolDirs() []string {
	if runtime.GOOS == "windows" {
		return fixedWindowsGoToolDirs()
	}

	dirs := []string{
		"/usr/local/go/bin",
		"/opt/homebrew/opt/go/libexec/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	}
	dirs = append(dirs, hostedToolcacheGoDirs()...)
	if os.Getenv("GOROOT") == "" {
		dirs = append([]string{filepath.Join(buildGoRootForVerify(), "bin")}, dirs...)
	}
	return dirs
}

func fixedWindowsGoToolDirs() []string {
	dirs := []string{
		`C:\Program Files\Go\bin`,
		`C:\Windows\System32`,
		`C:\Windows\System32\WindowsPowerShell\v1.0`,
	}
	if os.Getenv("GOROOT") == "" {
		dirs = append([]string{filepath.Join(buildGoRootForVerify(), "bin")}, dirs...)
	}
	return dirs
}

func hostedToolcacheGoDirs() []string {
	matches, err := filepath.Glob("/opt/hostedtoolcache/go/*/*/bin")
	if err != nil {
		return nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	return matches
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

func buildGoRootForVerify() string {
	// Used only when the parent process did not set GOROOT. In that case,
	// the build toolchain path lets runGoTest avoid user-controlled PATH.
	return runtime.GOROOT() //nolint:staticcheck
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
