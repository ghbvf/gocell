// Package cmdrun centralizes whitelisted subprocess invocation for
// governance and verify helpers (gocell validate / gocell check /
// golangci-lint runner / archtest tooling). Callers funnel through
// Run / RunWith so the gosec G204 path-scoped exemption lives at a single
// audit point and tool-path validation is enforced via the ValidatedTool
// newtype's unexported field (NewTool runs exec.LookPath + filepath.Abs +
// filepath.Clean).
//
// cmdrun is not the only subprocess entry point in the repo — tests in
// tools/generatedverify and examples/ssobff invoke exec.Command directly
// for `git`, `go build`, and the produced binary. Those sites are kept
// outside cmdrun because they exercise binary names that are constant
// strings under test control, not the LookPath-mediated whitelist that
// cmdrun guards. Production callers (anything outside *_test.go and
// outside examples/) must funnel through cmdrun.
package cmdrun

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ValidatedTool wraps a tool binary invocation path with two enforced
// invariants:
//
//  1. The path is exec.LookPath-resolved (the binary exists at construction
//     time, not at first Run — caller can fail-fast at startup).
//  2. The path is absolute (filepath.IsAbs == true). LookPath returns a
//     bare name when name has no path separators and PATH lookup succeeds,
//     or returns name as-is when name contains a separator. NewTool always
//     normalizes to filepath.Abs so the resolved binary cannot be hijacked
//     by later cwd changes or by a writable PATH directory inserted into
//     PATH after construction.
//
// The unexported `path` field forces normal API construction through
// NewTool — package consumers cannot legally produce a non-empty path
// bypassing the invariants. Zero values (constructed via reflect/unsafe
// or embedded literal `ValidatedTool{}`) carry an empty path, which makes
// Run fail-closed at exec.CommandContext with a "no such file or
// directory" error rather than panicking or executing arbitrary code.
type ValidatedTool struct{ path string }

// NewTool resolves name via exec.LookPath, normalizes to an absolute
// filepath.Clean'd form, and wraps the result in a ValidatedTool. Returns
// an error wrapping the LookPath failure when name cannot be found in
// PATH, or filepath.Abs failure (extremely rare — only when getwd fails).
func NewTool(name string) (ValidatedTool, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return ValidatedTool{}, fmt.Errorf("cmdrun: lookup %q: %w", name, err)
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return ValidatedTool{}, fmt.Errorf("cmdrun: resolve absolute path for %q: %w", name, err)
		}
		path = abs
	}
	return ValidatedTool{path: filepath.Clean(path)}, nil
}

// Dir returns the directory containing the tool binary. Callers use this
// for toolchain-locality concerns (e.g., prepending to PATH so subprocess
// helpers resolve to the same toolchain that owns the binary).
func (t ValidatedTool) Dir() string {
	return filepath.Dir(t.path)
}

// Run executes the validated tool with args using ctx and returns combined
// stdout+stderr. It inherits the parent process working directory and a
// denylist-cleaned copy of the parent environment.
func Run(ctx context.Context, t ValidatedTool, args ...string) ([]byte, error) {
	return RunWith(ctx, t, RunOptions{}, args...)
}

// RunOptions controls subprocess execution for RunWith.
type RunOptions struct {
	// Dir optionally sets the subprocess working directory. Empty means inherit.
	Dir string
	// ExtraEnv appends to, or overrides, the cleaned parent environment. Nil
	// and empty slices do not clear the inherited environment.
	ExtraEnv []string
}

// RunWith executes the validated tool with args. By default it inherits a
// denylist-cleaned copy of os.Environ; ExtraEnv is then applied additively so
// callers must explicitly opt sensitive values back in when a subprocess
// genuinely needs them. Combined stdout+stderr is returned.
//
// On Unix, RunWith starts the subprocess in its own process group (Setpgid)
// so that ctx cancellation kills the entire process tree, not just the direct
// child. This prevents orphaned grandchild processes (e.g. test subprocesses
// spawned by `go test`) from continuing to run after ctx is canceled.
//
// ValidatedTool.path is exec.LookPath-resolved at NewTool construction;
// args are caller-controlled whitelisted invocations from governance /
// verify helpers, never user input. gosec G204 on the variable binary
// path is a false positive in this context — addressed by the
// .golangci.yml path-scoped exemption rather than reshaping the
// invocation. See package doc for callers that bypass cmdrun (tests).
func RunWith(ctx context.Context, t ValidatedTool, opts RunOptions, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, t.path, args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	cmd.Env = mergeEnv(cleanEnv(os.Environ()), opts.ExtraEnv)
	// Place the subprocess in a new process group so that cancellation via
	// cmd.Cancel kills the entire group (including grandchildren). The
	// platform-specific Cancel func replaces exec.CommandContext's default
	// behavior of sending SIGKILL only to the direct child.
	cmd.SysProcAttr = newSysProcAttr()
	cmd.Cancel = func() error {
		return killProcessGroup(cmd.Process)
	}
	return cmd.CombinedOutput()
}

func cleanEnv(env []string) []string {
	cleaned := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || envKeyDenied(key) {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}

func mergeEnv(base, extra []string) []string {
	merged := append([]string(nil), base...)
	for _, entry := range extra {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			merged = append(merged, entry)
			continue
		}
		replaced := false
		for i, existing := range merged {
			existingKey, _, ok := strings.Cut(existing, "=")
			if ok && sameEnvKey(existingKey, key) {
				merged[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, entry)
		}
	}
	return merged
}

func envKeyDenied(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range []string{
		"PASSWORD",
		"PASSWD",
		"SECRET",
		"TOKEN",
		"PRIVATE_KEY",
		"ACCESS_KEY",
	} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func sameEnvKey(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
