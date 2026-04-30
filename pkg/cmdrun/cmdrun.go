// Package cmdrun centralizes whitelisted subprocess invocation for governance
// and verify tooling. All callers funnel through these helpers so the gosec
// G204 nolint annotation lives at a single audit point.
package cmdrun

import (
	"context"
	"os/exec"
)

// RunGit invokes "git" with whitelisted args (RefName / RevParse / etc.).
// CombinedOutput returns stdout + stderr.
func RunGit(ctx context.Context, args ...string) ([]byte, error) {
	//nolint:gosec // G204: hardcoded "git" + whitelisted args from RefName/RevParse helpers.
	return exec.CommandContext(ctx, "git", args...).CombinedOutput()
}

// RunGoTool invokes a validated go tool path (e.g., "go", "/usr/local/go/bin/go")
// with whitelisted args. Caller validates goTool at construction time.
func RunGoTool(ctx context.Context, goTool string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, goTool, args...).CombinedOutput() //nolint:gosec // G204: goTool path validated at construction
}
