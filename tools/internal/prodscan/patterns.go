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
// dirExists + IsModuleRoot prune entries that don't apply, and the shared
// satellite-aware loader (packagesload.LoadWorkspace) routes each surviving pattern
// to the right load mode — expanding a multi-member parent prefix to its members —
// so the EFFECTIVE scan differs per context without a per-context list:
//
//   - Real repo post-#1565: the core lives in the `framework` submodule, so
//     framework/{kernel,runtime,pkg} match (loaded ModeWorkspace — framework is a
//     sibling go.work member); the bare kernel/runtime/pkg no longer exist at the
//     root and are pruned; cmd/adapters/examples each hold SEVERAL go.work member
//     modules (multi-member parents), so their "./<dir>/…" pattern is owned by no
//     single member and is pruned by HasNestedModuleRoot from THIS base scan — it is
//     re-emitted ONLY by SatelliteParentPatterns, which callers compose in when they
//     want satellite coverage (the shared loader expands it to the members).
//     cellmodules/corecells are module roots → pruned by IsModuleRoot.
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
// Two kinds of top-level dir are pruned from this satellite-FREE base scan:
//
//   - A go.work satellite MODULE ROOT (carries its own go.mod — e.g. cellmodules/
//     after #1559, corecells/): a single module addressed by its own member
//     pattern, unaddressable by a root-relative ModeModule "./<dir>/..." (IsModuleRoot).
//   - A MULTI-MEMBER PARENT (cmd/, adapters/, examples/): not a module root itself,
//     but holding several go.work member modules one level deeper (cmd/gocell +
//     cmd/corebundle; adapters/postgres …; examples/<id> …). "./cmd/..." is owned by
//     no single member (HasNestedModuleRoot).
//
// Patterns is the conservative BASE: it omits the satellite prefixes so consumers
// that do not want satellite scope (the bulk of the production scanners) are not
// silently widened. Widening to satellites is a per-gate opt-in: a caller composes
// SatelliteParentPatterns onto Patterns (OBS-01) or PatternsExtended
// (PatternsWithSatellites), and the shared satellite-aware loader
// (packagesload.LoadWorkspace) expands each parent prefix to its real go.work
// members before loading. A single-module fixture writes cmd/pkg/... as part of its
// ONE module (no nested go.mod), so neither predicate fires there, the increment is
// empty, and the fixture's "./cmd/..." stays in Patterns and is scanned directly.
func Patterns(root string) []string {
	var patterns []string
	if dirHasNonTestGoFiles(root) {
		patterns = append(patterns, ".")
	}
	for _, dir := range topLevelDirs {
		full := filepath.Join(root, dir)
		if !dirExists(full) || IsModuleRoot(full) || HasNestedModuleRoot(full) {
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

// SatelliteParentPatterns returns the multi-member satellite parent prefixes
// ("./cmd/...", "./adapters/...", "./examples/...") that Patterns prunes — the
// single-sourced satellite increment. It is DERIVED from the same
// HasNestedModuleRoot predicate Patterns prunes on (not a hand-maintained list), so
// a future fourth multi-member parent is picked up automatically; it re-includes
// ONLY multi-member parents (plain module-root dirs cellmodules/, corecells/ stay
// excluded by !IsModuleRoot — they are addressed by their own member pattern, not an
// expandable parent prefix). A single-module fixture has no nested go.mod, so the
// increment is EMPTY there.
//
// The emitted prefixes are owned by no single go.work member and are loadable ONLY
// through the shared satellite-aware loader packagesload.LoadWorkspace, which expands
// each to its real members. Both the OBS-01 metric-PII scan (#2147) and the typeseval
// duration gates compose this increment onto their respective base scope; do NOT hand
// the prefixes to a loader that does not go through LoadWorkspace — a raw ModeModule
// load hard-errors and a non-expanding workspace load match-zeroes (silent coverage
// loss). This "must go through LoadWorkspace" is a Soft (godoc) convention: it is not
// type-enforced, but the loader's expansion is kept honest by the Medium archtest
// SATELLITE-PARENT-PREFIX-SCAN-01, which fails if a satellite prefix stops expanding
// to real members.
func SatelliteParentPatterns(root string) []string {
	var patterns []string
	for _, dir := range topLevelDirs {
		full := filepath.Join(root, dir)
		if dirExists(full) && !IsModuleRoot(full) && HasNestedModuleRoot(full) {
			patterns = append(patterns, "./"+dir+"/...")
		}
	}
	return patterns
}

// PatternsWithSatellites widens PatternsExtended(root) — Patterns plus tests/ and
// tools/ — with the multi-member satellite parent prefixes (SatelliteParentPatterns).
// It is the production-scan source-of-record for the typeseval-backed duration gates
// (TEST-TIME-LITERAL-01, PROD-DURATION-CONST-01), whose broad invariant covers
// production + test/tool support packages + satellites.
//
// Two satellite-bearing scopes exist, split by BASE (not loader capability — since
// #2147 both load through the same packagesload.LoadWorkspace expander):
//
//   - PatternsWithSatellites = PatternsExtended + satellites — the broad duration
//     gates (production + tests/ + tools/ + satellites).
//   - OBS-01 = Patterns + satellites — production + satellites, WITHOUT tests/+tools/
//     (composed in tools/metricschema; OBS-01 never scanned tests/ or tools/).
//
// Do NOT fold the satellite prefixes into PatternsExtended itself: its other
// consumers (e.g. errcode_invariants) would then silently gain unreviewed satellite
// scope. Widening to satellites is a per-gate opt-in by name, not a global default.
func PatternsWithSatellites(root string) []string {
	return append(PatternsExtended(root), SatelliteParentPatterns(root)...)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// IsModuleRoot reports whether dir carries its own go.mod (a go.work satellite
// module root). Such dirs are unaddressable by root-relative patterns under
// ModeModule (GOWORK=off) and are skipped by Patterns / PatternsExtended — see the
// Patterns godoc. Exported so coverage guards that must stay symmetric with the scan
// set (e.g. metricschema's productionGoTopLevels) share this single source of truth
// rather than re-deriving the go.mod check.
func IsModuleRoot(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil && !info.IsDir()
}

// HasNestedModuleRoot reports whether dir is a MULTI-MEMBER PARENT: it is not
// itself a module root, but at least one immediate subdirectory carries a go.mod
// (a go.work member). cmd/ (over cmd/gocell, cmd/corebundle), adapters/ (over
// adapters/postgres …), and examples/ (over examples/<id> …) are the post-#1565
// instances. A root-relative "./<dir>/..." pattern over such a parent is owned by no
// single member and cannot be loaded as a plain ModeModule pattern; Patterns prunes
// it from the base scope, and SatelliteParentPatterns re-emits it for callers that
// opt into satellite coverage, where packagesload.LoadWorkspace expands it to the
// members (#2147). Exported so the satellite increment and the OBS-01 coverage guard
// (metricschema's productionGoTopLevels, which now REQUIRES these parents) share this
// single source rather than re-deriving the nested-go.mod check. A single-module
// fixture has no nested go.mod under cmd/pkg/…, so this is false there and the fixture
// dir is kept in Patterns directly.
func HasNestedModuleRoot(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() && IsModuleRoot(filepath.Join(dir, entry.Name())) {
			return true
		}
	}
	return false
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
