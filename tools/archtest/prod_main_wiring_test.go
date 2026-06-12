//go:build archtest

// INVARIANT: PROD-MAIN-WIRING-NOOP-REJECT-01
//
// prod_main_wiring_test.go — dogfood + detector self-tests + anti-vacuity for
// PROD-MAIN-WIRING-NOOP-REJECT-01. The importable rule body lives in the non-test
// prod_main_wiring_noop_reject.go so external Cell repos run it via
// StandardCellRules / RunStandardCellRules; GoCell dogfoods the SAME Check here.
//
// Coverage:
//   - Dogfood: GoCell's production composition root (cmd/corebundle) is clean.
//   - Anti-vacuity: the workspace Production scan actually reaches the separate
//     cmd/corebundle module, so the dogfood green is real (not 0-files-scanned).
//   - Detector reds: per-symbol (qualified), import-alias, and dot-import fixtures
//     prove every forbidden symbol and every resolution form fires.
//   - Green: the sanctioned funnels (DemoCellEmitter / DemoCellTxManager /
//     NewDirectCellEmitter / ResolveEmitter / eventbus.New) are NOT flagged.
//   - matchesMainPkg unit table + empty-ProductionMainPkgs no-scan.

package archtest

import (
	"path"
	"path/filepath"
	"testing"
)

// prodMainWiringFixturePattern returns the (relDir, pattern) pair for a fixture
// case under prod_main_wiring_noop_fixtures.
func prodMainWiringFixturePattern(fix string) (dir, pattern string) {
	const fixturesDir = "prod_main_wiring_noop_fixtures"
	return filepath.Join("tools", "archtest", "testdata", fixturesDir, fix),
		"./tools/archtest/testdata/" + fixturesDir + "/" + fix
}

// TestProdMainWiringNoopReject_Dogfood runs the registered rule against GoCell's
// production composition root (cmd/corebundle) and asserts it is clean — the
// composition root must not directly construct any forbidden raw-noop sink.
func TestProdMainWiringNoopReject_Dogfood(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-wide ProdMainWiring dogfood in -short mode")
	}
	Report(t, ruleProdMainWiringNoopReject01,
		CheckProdMainWiringNoopReject(t, ConfigForExternalCell{
			ProductionMainPkgs: []string{"./cmd/corebundle"},
			BuildTags:          FlatNonDefaultTags(),
		}))
}

// prodMainWiringCorebundleFloor is the minimum number of cmd/corebundle production
// files the dogfood scan must observe. cmd/corebundle has ~19 production .go files
// at time of writing; a floor of 5 catches a near-empty regression (e.g. wiring
// silently moved to sub-packages that the exact "./cmd/corebundle" pattern no
// longer matches) without being brittle to ordinary file churn. If a legitimate
// refactor drops below this, update the floor AND reconsider whether the dogfood
// should use the recursive "./cmd/corebundle/..." pattern instead.
const prodMainWiringCorebundleFloor = 5

// TestProdMainWiringNoopReject_AntiVacuity_CorebundleInScope proves the workspace
// Production scan reaches the SEPARATE cmd/corebundle go.work module. Without this
// the dogfood above could be a silent vacuous-green (0 diagnostics because ~0 files
// were scanned, not because the wiring is clean). It asserts a FLOOR of production
// files under cmd/corebundle/ was matched by the main-pkg filter — not merely ≥1,
// so a refactor that shrinks the matched set to a single non-violating file cannot
// quietly hollow out the dogfood's coverage.
func TestProdMainWiringNoopReject_AntiVacuity_CorebundleInScope(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping workspace scan in -short mode")
	}
	n := prodMainWiringFilesInScope(t, []string{"./cmd/corebundle"})
	if n < prodMainWiringCorebundleFloor {
		t.Fatalf("PROD-MAIN-WIRING-NOOP-REJECT-01 anti-vacuity: workspace Production scan observed %d "+
			"cmd/corebundle production files (floor %d) — the dogfood is hollow / vacuous-green. Either the "+
			"go.work Production loader is not reaching the cmd/corebundle module, or the package shrank; fix "+
			"the scan (or update the floor + pattern) before trusting the green.", n, prodMainWiringCorebundleFloor)
	}
}

// prodMainWiringFilesInScope counts production files the main-pkg filter matches
// for the given patterns across the workspace Production scan. Used only by the
// anti-vacuity self-test.
func prodMainWiringFilesInScope(t *testing.T, mainPkgs []string) int {
	t.Helper()
	count := 0
	_ = Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || len(p.Files) == 0 {
			return nil
		}
		if matchesMainPkg(path.Dir(p.Rel(p.Files[0])), mainPkgs) {
			count += len(p.Files)
		}
		return nil
	})
	return count
}

// TestProdMainWiringNoopReject_Detector_RedQualified asserts the detector fires
// on all five forbidden symbols referenced in qualified form (one fixture, five
// diagnostics). Detector self-test: it runs the raw reference scanner
// (collectProdMainWiringViolations) directly, bypassing the main-pkg filter, so
// the fixture need not live under a main-pkg dir.
func TestProdMainWiringNoopReject_Detector_RedQualified(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := prodMainWiringFixturePattern("red_qualified")
	diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), collectProdMainWiringViolations)
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestProdMainWiringNoopReject_Detector_RedAliasImport asserts the detector is
// alias-proof: an aliased import of kernel/outbox does not hide NewNoopEmitter.
func TestProdMainWiringNoopReject_Detector_RedAliasImport(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := prodMainWiringFixturePattern("red_aliasimport")
	diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), collectProdMainWiringViolations)
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestProdMainWiringNoopReject_Detector_RedDotImport asserts the detector's
// ast.Ident walk catches the dot-import bare-identifier form of NoopWriter.
func TestProdMainWiringNoopReject_Detector_RedDotImport(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := prodMainWiringFixturePattern("red_dotimport")
	diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), collectProdMainWiringViolations)
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestProdMainWiringNoopReject_Green_Sanctioned asserts the detector does NOT
// flag the sanctioned funnels the rule is meant to push raw noop through
// (DemoCellEmitter / DemoCellTxManager / NewDirectCellEmitter / ResolveEmitter /
// eventbus.New). Empty golden = zero diagnostics.
func TestProdMainWiringNoopReject_Green_Sanctioned(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := prodMainWiringFixturePattern("green_sanctioned")
	diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), collectProdMainWiringViolations)
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestProdMainWiringNoopReject_BlindSpot_FactoryWrapping is the reverse self-test
// for the cross-package call-graph transitivity blind spot (charter §强制盲区自检):
// a composition root that obtains a raw noop sink by calling a factory in ANOTHER
// package (blindspot_factory_helper) — never referencing a forbidden symbol
// directly — is NOT caught by the callsite scan. Scanning only the caller yields
// ZERO diagnostics (empty golden), making the documented permanent Go ceiling
// visible and regression-pinned. The runtime CheckNotNoop guard (durable mode) is
// the complementary defense for this gap.
func TestProdMainWiringNoopReject_BlindSpot_FactoryWrapping(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := prodMainWiringFixturePattern("blindspot_factory_caller")
	diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), collectProdMainWiringViolations)
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestProdMainWiringNoopReject_EmptyPkgs_NoScan asserts the opt-in contract: an
// empty ProductionMainPkgs means "do not scan composition roots" (mirrors
// BuildTags), so the Check returns no diagnostics without a workspace scan.
func TestProdMainWiringNoopReject_EmptyPkgs_NoScan(t *testing.T) {
	t.Parallel()
	if diags := CheckProdMainWiringNoopReject(t, ConfigForExternalCell{}); len(diags) != 0 {
		t.Errorf("empty ProductionMainPkgs must produce no diagnostics, got %d: %+v", len(diags), diags)
	}
}

// TestMatchesMainPkg covers the module-path-agnostic main-pkg matcher: exact,
// ./-prefixed, dir/... recursive, dir-boundary non-match, and empty patterns.
func TestMatchesMainPkg(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		relDir   string
		patterns []string
		want     bool
	}{
		{"exact-dotslash", "cmd/corebundle", []string{"./cmd/corebundle"}, true},
		{"exact-plain", "cmd/corebundle", []string{"cmd/corebundle"}, true},
		{"non-match-sibling", "cmd/gocell", []string{"./cmd/corebundle"}, false},
		{"recursive-self", "cmd/corebundle", []string{"./cmd/corebundle/..."}, true},
		{"recursive-sub", "cmd/corebundle/internal", []string{"./cmd/corebundle/..."}, true},
		{"recursive-prefix-boundary", "cmd/corebundlex", []string{"./cmd/corebundle/..."}, false},
		{"exact-prefix-boundary", "cmd/corebundlex", []string{"./cmd/corebundle"}, false},
		{"empty-patterns", "cmd/corebundle", nil, false},
		{"second-pattern-matches", "cmd/corebundle", []string{"./cmd/gocell", "./cmd/corebundle"}, true},
		// Deliberately unsupported (each matches nothing — by design; see matchesMainPkg godoc):
		{"whole-module-dotdotdot-unsupported", "cmd/corebundle", []string{"./..."}, false},
		{"whole-module-dot-unsupported", "cmd/corebundle", []string{"."}, false},
		{"absolute-path-no-match", "cmd/corebundle", []string{"/abs/cmd/corebundle"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := matchesMainPkg(c.relDir, c.patterns); got != c.want {
				t.Errorf("matchesMainPkg(%q, %v) = %v, want %v", c.relDir, c.patterns, got, c.want)
			}
		})
	}
}
