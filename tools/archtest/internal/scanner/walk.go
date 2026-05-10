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
//
// Symlinks are rejected fail-loud: archtest scans the static repository
// structure, so any symlink under modRoot (the root itself or an entry
// surfaced by the walk) signals either a misconfigured caller or an attempt
// to redirect the scan to arbitrary host content. Silently skipping a symlink
// would let an evil_gen.go → /etc/passwd entry be compiled by Go tooling
// (which follows the link) while bypassing every archtest gate. Returning an
// error makes the misconfiguration visible in CI immediately.
func walkFiles(modRoot, root string, skipDirs map[string]struct{}, accept func(path string) bool) ([]string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("lstat: %s: %w", moduleRelDisplay(modRoot, root), err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errSymlink(modRoot, root)
	}

	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", moduleRelDisplay(modRoot, path), walkErr)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return errSymlink(modRoot, path)
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

// errSymlink is the single error returned by walkFiles whenever a symlink is
// encountered (root or entry, file or directory). The message carries a
// module-relative path so it stays useful in CI logs without leaking absolute
// host paths, and the wording is unambiguous about the fail-loud intent.
func errSymlink(modRoot, path string) error {
	return fmt.Errorf("walk: %s is a symlink (refused; archtest scans the static repository structure, real files only)",
		moduleRelDisplay(modRoot, path))
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
