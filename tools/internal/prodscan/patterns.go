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
//     modules (multi-member parents), so their "./<dir>/…" pattern is owned by no
//     single member and is pruned by HasNestedModuleRoot from THIS satellite-free
//     scan (metricschema's OBS-01 loader cannot expand a multi-member prefix). The
//     typeseval-backed duration gates opt INTO satellite coverage via
//     PatternsWithSatellites; OBS-01 metric-PII coverage of satellites stays gh
//     #2136. cellmodules/corecells are module roots → pruned by IsModuleRoot.
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
// Two kinds of top-level dir are pruned from this satellite-FREE scan:
//
//   - A go.work satellite MODULE ROOT (carries its own go.mod — e.g. cellmodules/
//     after #1559, corecells/): a single module addressed by its own member
//     pattern, unaddressable by a root-relative ModeModule "./<dir>/..." (IsModuleRoot).
//   - A MULTI-MEMBER PARENT (cmd/, adapters/, examples/): not a module root itself,
//     but holding several go.work member modules one level deeper (cmd/gocell +
//     cmd/corebundle; adapters/postgres …; examples/<id> …). "./cmd/..." is owned by
//     no single member, so post-#1565 (no root module to anchor it) it would
//     hard-error or silently match-zero (HasNestedModuleRoot).
//
// Both are pruned so this PRODUCTION scan stays loadable under metricschema's OBS-01
// loader, which loads each pattern in ModeModule/ModeWorkspace and does NOT expand a
// multi-member parent prefix. A single-module fixture writes cmd/pkg/... as part of
// its ONE module (no nested go.mod), so neither predicate fires there and the
// fixture's "./cmd/..." is kept and scanned.
//
// Rules that DO require satellite coverage opt in through the typed (typeseval)
// loader, which expands a satellite parent prefix to its real go.work members via
// workspace.ExpandParentPrefix (kept honest by SATELLITE-PARENT-PREFIX-SCAN-01):
//
//   - the broad duration gates (TEST-TIME-LITERAL-01, PROD-DURATION-CONST-01) take
//     the whole satellite set via PatternsWithSatellites; and
//   - narrow security/authz funnels (fence-token mint, …) hardcode the specific
//     satellite patterns they need.
//
// Both opt-in paths are independent of THIS list — Patterns deliberately does NOT
// emit the satellite prefixes, since OBS-01's loader cannot expand them (its own
// metric-PII coverage of satellites remains gh #2136).
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

// PatternsWithSatellites widens PatternsExtended(root) with the multi-member
// satellite parent prefixes ("./cmd/...", "./adapters/...", "./examples/...") that
// Patterns deliberately prunes. It is the production-scan source-of-record for the
// typed (typeseval) duration gates — TEST-TIME-LITERAL-01 and PROD-DURATION-CONST-01
// — that load via tools/archtest's typed façade, whose workspace.ExpandParentPrefix
// expands each satellite parent prefix to its real go.work members before loading
// (kept honest by SATELLITE-PARENT-PREFIX-SCAN-01). Only that path can actually scan
// the satellites, so only this constructor emits their prefixes (#2136).
//
// Two scan constructors exist, split by LOADER CAPABILITY — not by accident — and
// they must NOT be merged (doing so re-breaks metricschema):
//
//   - Patterns / PatternsExtended (satellite-FREE): consumed by metricschema's
//     OBS-01 loader, which does NOT expand a multi-member parent prefix; handing it
//     "./cmd/..." (owned by no single go.work member post-#1565) hard-errors or
//     match-zeroes, so the satellite parents are pruned there.
//   - PatternsWithSatellites: consumed only by the typeseval-backed duration gates,
//     which CAN expand the satellite prefixes.
//
// The satellite increment is DERIVED from the same HasNestedModuleRoot predicate
// Patterns prunes on — not a hand-maintained list — so a future fourth multi-member
// parent is picked up automatically. A single-module fixture has no nested go.mod, so
// the increment is empty there and this equals PatternsExtended.
//
// Do NOT fold the satellite prefixes into PatternsExtended itself: its other
// consumers (e.g. errcode_invariants) would then silently gain unreviewed satellite
// scope. Widening to satellites is a per-gate opt-in by name, not a global default.
func PatternsWithSatellites(root string) []string {
	patterns := PatternsExtended(root)
	for _, dir := range topLevelDirs {
		full := filepath.Join(root, dir)
		if dirExists(full) && !IsModuleRoot(full) && HasNestedModuleRoot(full) {
			patterns = append(patterns, "./"+dir+"/...")
		}
	}
	return patterns
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// IsModuleRoot reports whether dir carries its own go.mod (a go.work satellite
// module root). Such dirs are unaddressable by root-relative patterns under
// ModeModule (GOWORK=off) and are skipped by Patterns / PatternsExtended — see
// the Patterns godoc + gh #2136. Exported so coverage guards that must stay
// symmetric with the scan set (e.g. metricschema's productionGoTopLevels) share
// this single source of truth rather than re-deriving the go.mod check.
func IsModuleRoot(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil && !info.IsDir()
}

// HasNestedModuleRoot reports whether dir is a MULTI-MEMBER PARENT: it is not
// itself a module root, but at least one immediate subdirectory carries a go.mod
// (a go.work member). cmd/ (over cmd/gocell, cmd/corebundle), adapters/ (over
// adapters/postgres …), and examples/ (over examples/<id> …) are the post-#1565
// instances. A root-relative "./<dir>/..." pattern over such a parent is owned by
// no single member and cannot be loaded as a ModeModule pattern (no root module to
// anchor it); its production-scan coverage is deferred to gh #2136. Patterns skips
// these dirs and the OBS-01 coverage guard (metricschema's productionGoTopLevels)
// must skip them symmetrically — exported so both share this single source rather
// than re-deriving the nested-go.mod check. A single-module fixture has no nested
// go.mod under cmd/pkg/…, so this is false there and the fixture dir is kept.
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
