// Package governance hosts repository-level audit and validation rules. The
// helpers in this file expose a single source of truth for "what does git
// HEAD know about this path" so generatedverify and metricschema cannot
// drift into divergent definitions of "tracked".
package governance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/ghbvf/gocell/pkg/cmdrun"
)

// gitTool resolves the git binary once via cmdrun.NewTool (exec.LookPath).
// First-call failure is cached so subsequent calls fail-fast with the same
// error rather than silently degrading.
var gitTool = sync.OnceValues(func() (cmdrun.ValidatedTool, error) {
	return cmdrun.NewTool("git")
})

// runGit invokes the resolved git binary with args. ctx flows directly into
// cmdrun.RunWith so caller deadlines/cancellations propagate to the
// subprocess (exec.CommandContext kills the child on ctx.Done()), avoiding
// indefinite hangs on slow filesystems (NFS / FUSE).
func runGit(ctx context.Context, args ...string) ([]byte, error) {
	tool, err := gitTool()
	if err != nil {
		return nil, err
	}
	return cmdrun.RunWith(ctx, tool, cmdrun.RunOptions{}, args...)
}

// HasGitMetadata reports whether root looks like a git work tree (has a .git
// entry). Test fixtures that operate on plain temp directories return false
// so callers can degrade gracefully to content-only checks.
func HasGitMetadata(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

// hasHEAD reports whether root has a resolvable HEAD ref. An empty
// repository (no commits yet) returns false. Used by HEAD-querying helpers
// to short-circuit before invoking git commands that would fail with
// "unable to resolve revision".
//
// Returns (false, error) when ctx is canceled or deadline exceeded so
// callers do not silently mistake a canceled probe for "no HEAD" — that
// would let a downstream reverse-enumeration skip the entire HEAD scan
// after the operator hit Ctrl+C.
func hasHEAD(ctx context.Context, root string) (bool, error) {
	_, err := runGit(ctx, "-C", root, "rev-parse", "--verify", "--quiet", "HEAD")
	if err == nil {
		return true, nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return false, cerr
	}
	// Any non-ctx error (including ExitError for "no HEAD" / "not a repo")
	// is a probe failure — the caller treats it as "no HEAD" and continues.
	return false, nil
}

// CommittedInHEAD reports whether rel is committed in HEAD at root. Files
// that are only `git add`-ed (in the index but not in HEAD) return false so
// every committed-in-HEAD audit gate uses a uniform predicate.
//
// rel must be a forward-slash repo-relative path. ExitErrors are interpreted
// as "not committed" (cat-file -e exits non-zero for unknown refs); other
// errors propagate so the caller fails closed. ctx.Err() takes precedence
// over the ExitError mapping so a canceled subprocess never gets folded
// into "not committed".
func CommittedInHEAD(ctx context.Context, root, rel string) (bool, error) {
	_, err := runGit(ctx, "-C", root, "cat-file", "-e", "HEAD:"+rel)
	if err == nil {
		return true, nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return false, cerr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, fmt.Errorf("git cat-file HEAD:%s: %w", rel, err)
}
