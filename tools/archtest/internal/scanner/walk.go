package scanner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// walkFiles walks root, skipping any directory whose base name appears in
// skipDirs. accept is consulted on every non-directory entry; paths for which
// accept(path) returns true are returned. Any walk error is wrapped and
// returned immediately (fail-closed). Non-existent root returns nil slice +
// nil error. modRoot is used to compute module-relative paths in error
// messages so that absolute paths do not appear in CI logs unexpectedly.
func walkFiles(modRoot, root string, skipDirs map[string]struct{}, accept func(path string) bool) ([]string, error) {
	if _, err := os.Lstat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		display := moduleRelDisplay(modRoot, root)
		return nil, fmt.Errorf("lstat: %s: %w", display, err)
	}

	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			display := moduleRelDisplay(modRoot, path)
			return fmt.Errorf("walk %s: %w", display, walkErr)
		}
		// fail-closed: archtest scans the static repository structure, so symlinks
		// are never a legitimate input. filepath.WalkDir does not descend into
		// symlink directories on its own, but it still surfaces symlink files via
		// the callback. Reject every entry whose Type bit is ModeSymlink so a
		// malicious or accidental symlink under modRoot cannot redirect a scan to
		// arbitrary host content. d.IsDir() is always false here (a symlink's
		// Type does not carry ModeDir even when the target is a directory), so a
		// single return nil suffices.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return skipDirCheck(d.Name(), skipDirs)
		}
		if accept(path) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// moduleRelDisplay returns a module-relative display string for path.
// If the relative computation fails, it returns the original absolute path
// with a "rel-failed:" prefix so callers know the value may be absolute.
func moduleRelDisplay(modRoot, path string) string {
	rel, err := filepath.Rel(modRoot, path)
	if err != nil {
		return "rel-failed:" + path
	}
	return filepath.ToSlash(rel)
}

// skipDirCheck returns filepath.SkipDir if name is in skipDirs, else nil.
func skipDirCheck(name string, skipDirs map[string]struct{}) error {
	if _, skip := skipDirs[name]; skip {
		return filepath.SkipDir
	}
	return nil
}

// isGoFile reports whether path is a Go source file that should be included.
func isGoFile(path string, includeTests bool) bool {
	if !strings.HasSuffix(path, ".go") {
		return false
	}
	if !includeTests && strings.HasSuffix(path, "_test.go") {
		return false
	}
	return true
}

// matchesSuffix reports whether path ends with any of suffixes (exact-string
// suffix match, case-sensitive). Used by Scope.contentFiles to filter on
// file extensions like ".yaml" / ".sql" / ".md".
func matchesSuffix(path string, suffixes []string) bool {
	for _, s := range suffixes {
		if strings.HasSuffix(path, s) {
			return true
		}
	}
	return false
}
