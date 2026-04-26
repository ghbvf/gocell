package archtest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// findCellProductionGoFiles walks cells/** for production .go files
// (excluding _test.go + vendor + .git + testdata + generated). Used by both
// the outbox topic scanner (via packages.Load) and by AST-only scanners in
// repoerr_test.go that do not require type information.
func findCellProductionGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "cells"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "worktrees", "testdata", "generated", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}
