package prodscan

import (
	"os"
	"path/filepath"
	"strings"
)

// topLevelDirs is the candidate production-scan scope. It lists every top-level
// directory that may hold production packages in EITHER context this scan runs in:
//
//   - the real gocell repo (a multi-module workspace), and
//   - a single-module consumer/fixture project (the metricschema OBS-01 fixtures,
//     a downstream module that vendors gocell).
//
// dirExists + IsModuleRoot prune entries that don't apply, and patternLoadMode
// (in tools/metricschema) routes each surviving pattern to the right load mode, so
// the EFFECTIVE scan differs per context without a per-context list:
//
//   - Real repo post-#1565: the core lives in the `framework` submodule, so
//     framework/{kernel,runtime,pkg} match (loaded ModeWorkspace — framework is a
//     sibling go.work member); the bare kernel/runtime/pkg no longer exist at the
//     root and are pruned; cmd/adapters/examples each hold SEVERAL go.work member
//     modules, so their "./<dir>/…" pattern is owned by no single member — the typed
//     loaders (tools/archtest/internal/typeseval + tools/metricschema) expand it to
//     those members via workspace.ExpandParentPrefix and load each in ModeWorkspace,
//     so the satellites ARE scanned (the cross-module coverage once deferred to gh
//     #1590). cellmodules/corecells are module roots → pruned by IsModuleRoot.
//   - Single-module fixture: framework/<layer> doesn't exist (pruned); the bare
//     cmd/pkg/kernel/runtime the fixture writes ARE part of the one module, so their
//     "./<dir>/…" pattern matches under ModeModule and is actually scanned.
var topLevelDirs = []string{
	"framework/kernel",
	"framework/runtime",
	"framework/pkg",
	"cmd",
	"kernel",
	"runtime",
	"pkg",
	"adapters",
	"cells",
	"cellmodules",
	"examples",
}

// Patterns returns the repository production package patterns used by typed
// governance scanners. Missing top-level directories are skipped so temp-module
// fixtures can exercise the production entrypoints without synthetic dirs.
//
// A top-level dir that is itself a go.work satellite MODULE ROOT (carries its own
// go.mod — e.g. cellmodules/ after #1559) is skipped: it is a single module
// addressed by its own member pattern, not a multi-member parent prefix. cmd/,
// adapters/, and examples/ are NOT module roots (their go.mod live one level deeper
// in cmd/gocell, adapters/postgres, examples/<id>, …), so they stay as "./<dir>/..."
// parent prefixes. Each such prefix spans several members; the typed loaders expand
// it to its real members (workspace.ExpandParentPrefix) and load them in
// ModeWorkspace, so the satellite packages are actually scanned — anti-vacuity
// guarded by SATELLITE-PARENT-PREFIX-SCAN-01. (Pre-#1565 these prefixes match-zeroed
// under the root module; post-#1565, with no root module, an un-expanded "./cmd/..."
// would hard-error under ModeModule or be silently skipped.)
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
