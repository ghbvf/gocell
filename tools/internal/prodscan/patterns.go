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
	"cellmodules",
	"examples",
	"pkg",
}

// Patterns returns the repository production package patterns used by typed
// governance scanners. Missing top-level directories are skipped so temp-module
// fixtures can exercise the production entrypoints without synthetic dirs.
//
// A top-level dir that is itself a go.work satellite MODULE ROOT (carries its own
// go.mod — e.g. cellmodules/ after #1559) is skipped: these patterns are consumed
// under ModeModule (GOWORK=off), where a root-relative "./<dir>/..." pointing at a
// foreign module root hard-errors "directory prefix <dir> does not contain main
// module". cmd/ and examples/ are NOT module roots (their go.mod live one level
// deeper in cmd/gocell, cmd/corebundle, examples/<id>), so "./cmd/..." /
// "./examples/..." merely match-zero the satellite subtrees instead of erroring —
// the satellite code is already outside GOWORK=off scans. cellmodules/ joins them
// as a satellite invisible to ModeModule scans; full cross-module coverage of every
// satellite is the ModeWorkspace migration tracked by gh #1590.
func Patterns(root string) []string {
	var patterns []string
	if dirHasNonTestGoFiles(root) {
		patterns = append(patterns, ".")
	}
	for _, dir := range topLevelDirs {
		full := filepath.Join(root, dir)
		if !dirExists(full) || IsModuleRoot(full) {
			continue
		}
		patterns = append(patterns, "./"+dir+"/...")
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

// PatternsExtended widens Patterns(root) to include tests/ and tools/ —
// used by gates whose invariant covers production-like support packages
// shipped with build-tag gating (e.g. tests/e2e/internal/clients).
func PatternsExtended(root string) []string {
	base := Patterns(root)
	if dirExists(filepath.Join(root, "tests")) {
		base = append(base, "./tests/...")
	}
	if dirExists(filepath.Join(root, "tools")) {
		base = append(base, "./tools/...")
	}
	return base
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// IsModuleRoot reports whether dir carries its own go.mod (a go.work satellite
// module root). Such dirs are unaddressable by root-relative patterns under
// ModeModule (GOWORK=off) and are skipped by Patterns / PatternsExtended — see
// the Patterns godoc + gh #1590. Exported so coverage guards that must stay
// symmetric with the scan set (e.g. metricschema's productionGoTopLevels) share
// this single source of truth rather than re-deriving the go.mod check.
func IsModuleRoot(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil && !info.IsDir()
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
