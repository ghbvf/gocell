// Package governance hosts repository-level audit and validation rules. The
// helpers in this file expose a single source of truth for "what does git
// HEAD know about this path" so generatedverify and metricschema cannot
// drift into divergent definitions of "tracked".
package governance

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// gitExecutable resolves git's absolute path once via PATH lookup and
// caches the result. SonarCloud / gosec G204 flag exec.Command("git", ...)
// because PATH may include writable directories where an attacker could
// plant a malicious binary; resolving the path at first use narrows the
// exposure window to package initialization, after which every git
// invocation goes through the absolute path captured here. Returns "git"
// when LookPath fails so callers still produce an honest "executable file
// not found" error from exec.Run.
var gitExecutable = sync.OnceValue(func() string {
	if path, err := exec.LookPath("git"); err == nil {
		return path
	}
	return "git"
})

// gitCmd builds an exec.Cmd that invokes the cached git binary. All git
// invocations in this package go through this constructor so PATH
// resolution lives in one place and the SAST suppression rationale stays
// in one well-documented spot.
func gitCmd(args ...string) *exec.Cmd {
	return exec.Command(gitExecutable(), args...)
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
func hasHEAD(root string) bool {
	return gitCmd("-C", root, "rev-parse", "--verify", "--quiet", "HEAD").Run() == nil
}

// CommittedInHEAD reports whether rel is committed in HEAD at root. Files
// that are only `git add`-ed (in the index but not in HEAD) return false so
// every committed-in-HEAD audit gate uses a uniform predicate.
//
// rel must be a forward-slash repo-relative path. ExitErrors are interpreted
// as "not committed" (cat-file -e exits non-zero for unknown refs); other
// errors propagate so the caller fails closed.
func CommittedInHEAD(root, rel string) (bool, error) {
	cmd := gitCmd("-C", root, "cat-file", "-e", "HEAD:"+rel)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("git cat-file HEAD:%s: %w", rel, err)
	}
	return true, nil
}
