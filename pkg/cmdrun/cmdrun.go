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

// ValidatedTool wraps an exec.LookPath-resolved absolute path to a tool
// binary. The unexported `path` field forces normal API construction
// through NewTool — package consumers cannot legally produce a non-empty
// path bypassing LookPath. Zero values (constructed via reflect/unsafe or
// embedded literal `ValidatedTool{}`) carry an empty path, which makes
// Run fail-closed at exec.CommandContext with a "no such file or
// directory" error rather than panicking or executing arbitrary code.
type ValidatedTool struct{ path string }

// NewTool resolves name via exec.LookPath and wraps the result in a
// ValidatedTool. Returns an error wrapping the LookPath failure when name
// cannot be found in PATH.
func NewTool(name string) (ValidatedTool, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return ValidatedTool{}, fmt.Errorf("cmdrun: lookup %q: %w", name, err)
	}
	return ValidatedTool{path: path}, nil
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
// G204 nolint rationale: ValidatedTool.path is exec.LookPath-resolved at
// NewTool construction (the single audit point for subprocess invocation
// in this repo); args are caller-controlled whitelisted invocations from
// governance/verify helpers, never user input.
//
//nolint:gosec // G204: ValidatedTool.path validated at NewTool construction (see docblock).
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
