package scanner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// walkGoFiles walks root, skipping any directory whose base name appears in
// skipDirs. If includeTests is false, files ending in _test.go are excluded.
// Any walk error is wrapped and returned immediately (fail-closed).
// If root does not exist, an empty slice is returned with no error.
func walkGoFiles(root string, skipDirs map[string]struct{}, includeTests bool) ([]string, error) {
	// Silently skip non-existent roots (e.g. DirsScope with a missing directory).
	if _, err := os.Lstat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}

	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if d.IsDir() {
			return skipDirCheck(d.Name(), skipDirs)
		}
		if isGoFile(path, includeTests) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
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
