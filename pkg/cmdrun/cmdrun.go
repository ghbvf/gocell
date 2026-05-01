// Package cmdrun centralizes whitelisted subprocess invocation. All callers
// funnel through Run/RunIn so the gosec G204 nolint annotation lives at a
// single audit point and tool-path validation is enforced via the
// ValidatedTool newtype's unexported field.
package cmdrun

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
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
// stdout+stderr. No Dir/Env customization — inherits the parent process
// working directory and environment.
func Run(ctx context.Context, t ValidatedTool, args ...string) ([]byte, error) {
	return RunIn(ctx, t, "", nil, args...)
}

// RunIn executes the validated tool with args, optionally overriding the
// working directory (empty dir = inherit) and environment (nil env =
// inherit os.Environ). Combined stdout+stderr is returned.
//
// ValidatedTool.path is exec.LookPath-resolved at NewTool construction (the
// single audit point for subprocess invocation in this repo); args are
// caller-controlled whitelisted invocations from governance/verify helpers,
// never user input. gosec G204 on the variable binary path is a false
// positive in this exact context — addressed by .golangci.yml gosec.excludes
// or a path-scoped exemption rather than reshaping the invocation.
func RunIn(ctx context.Context, t ValidatedTool, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, t.path, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	return cmd.CombinedOutput()
}
