package prodscan

import (
	"os"
	"path/filepath"
	"strings"
)

var topLevelDirs = []string{
	"cmd",
	"kernel",
	"runtime",
	"adapters",
	"cells",
	"examples",
	"pkg",
}

// Patterns returns the repository production package patterns used by typed
// governance scanners. Missing top-level directories are skipped so temp-module
// fixtures can exercise the production entrypoints without synthetic dirs.
func Patterns(root string) []string {
	var patterns []string
	if dirHasNonTestGoFiles(root) {
		patterns = append(patterns, ".")
	}
	for _, dir := range topLevelDirs {
		if dirExists(filepath.Join(root, dir)) {
			patterns = append(patterns, "./"+dir+"/...")
		}
	}
	return patterns
}

// PatternTopLevels maps production scan patterns back to their top-level
// directory names for coverage-guard tests.
func PatternTopLevels(patterns []string) map[string]bool {
	out := map[string]bool{}
	for _, pattern := range patterns {
		if pattern == "." {
			out["."] = true
			continue
		}
		trimmed := strings.TrimPrefix(pattern, "./")
		trimmed = strings.TrimSuffix(trimmed, "/...")
		if trimmed == "" {
			continue
		}
		out[strings.Split(trimmed, "/")[0]] = true
	}
	return out
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func dirHasNonTestGoFiles(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			return true
		}
	}
	return false
}
