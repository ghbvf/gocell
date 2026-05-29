package archtest

import "testing"

// external.go — the importable public surface that lets an external Cell
// repository (Operator-SDK or Workspace mode, see
// docs/architecture/202605281200-adr-cell-development-external-repo.md §R1/M3)
// run GoCell's curated architecture invariants against its OWN module from a
// plain `go test`, without copying rule code.
//
// Design is modeled on golang.org/x/tools/go/analysis:
//
//   - [CellRule] is the importable rule descriptor — the analog of
//     analysis.Analyzer. The rule LOGIC lives in non-test .go files (so a
//     dependency can compile it; Go never compiles a dependency's _test.go),
//     keyed by a stable ID and a Run closure. ref: go/analysis Analyzer.
//   - [StandardCellRules] returns the curated portable set as a flat slice —
//     the analog of the []*analysis.Analyzer passed to multichecker.Main.
//     There is no registry and no init() side-effect; composition is a slice.
//     ref: go/analysis/multichecker.
//   - [RunStandardCellRules] is the single entry an external repo calls — the
//     analog of ArchUnit's ArchTests.in(StandardRules.class) driven against
//     the consumer's @AnalyzeClasses scan scope. The scan TARGET is supplied
//     by the running module (RunTyped → findModuleRoot walks up from the test
//     process cwd, so it resolves the consumer's own go.mod), never baked into
//     the rule. ref: ArchUnit user guide; arch-go config.Load(modulePath).
//
// Platform-vs-scan path split (the core correctness invariant of M3):
//
//   - A rule's references to GoCell PLATFORM symbols (errcode, redaction,
//     panicregister, …) stay anchored to [PlatformModulePath] because an
//     external repo imports those packages as a dependency at that fixed path
//     — exactly like go/analysis's printf analyzer hardcoding "fmt.Printf" and
//     copylock hardcoding "sync" (stable dependency paths).
//   - A rule's SCAN SCOPE (which packages it walks for violations) is the
//     module under analysis, supplied by the driver — the analog of
//     analysis.Pass.Pkg coming from the loaded target, not a constant.
//
// GoCell dogfoods the exact same Check* functions these CellRules wrap (single
// source): each per-rule Test in this package calls the shared Check* func, and
// RunStandardCellRules calls the same func — there is no parallel rule body.

// PlatformModulePath is the Go module path of the GoCell platform itself. It is
// the ONE sanctioned site for the "github.com/ghbvf/gocell" string literal in
// this package: every rule that resolves a GoCell platform symbol path must
// derive it as PlatformModulePath+"/pkg/…" rather than hardcoding the literal,
// so a module rename / /v2 bump updates exactly one place and the ratchet
// meta-archtest ARCHTEST-MODULE-PATH-FUNNEL-01 can prove no rule reintroduces a
// bare literal. It is NOT the scan target: the module being analyzed is
// supplied by the driver (RunTyped/RunTypedProduction → findModuleRoot), which
// resolves the consumer's own go.mod for external repos.
const PlatformModulePath = "github.com/ghbvf/gocell"

// CellRule is a reusable, importable architecture invariant — the GoCell
// analog of golang.org/x/tools/go/analysis.Analyzer. ID is the stable rule
// identifier reported via [Report] (e.g. "PANIC-REGISTERED-01"); Run executes
// the rule against the module described by cfg and returns the diagnostics it
// observed (it does NOT call t.Errorf itself — [RunStandardCellRules] funnels
// every rule's diagnostics through [Report] uniformly).
//
// Run takes *testing.T because the underlying drivers (Run / RunTyped /
// RunTypedProduction) fail-loud via t.Fatalf on load errors and need the test
// handle. A rule whose scan is module-wide may ignore cfg; rules that need the
// consumer's own composition-root packages read cfg.ProductionMainPkgs.
type CellRule struct {
	// ID is the stable rule identifier (e.g. "PANIC-REGISTERED-01"). It is the
	// ruleID passed to Report and must be unique within a rule set.
	ID string
	// Run executes the rule and returns its diagnostics. Must be non-nil.
	Run func(t *testing.T, cfg ConfigForExternalCell) []Diagnostic
}

// ConfigForExternalCell is the consumer-supplied description of the module that
// [RunStandardCellRules] should analyze. The module IMPORT PATH and ROOT are
// not fields the consumer must get right: the drivers resolve them from the
// running module's go.mod (findModuleRoot walks up from the test process cwd),
// which is fail-closed by construction — an unreadable/absent go.mod fails the
// underlying driver loudly. This mirrors M2's render.go contract (module path
// is derived, never a silently-defaulted input). ref: arch-go config.Load.
//
// Fields are deliberately minimal (YAGNI): a metadata-locator Strategy and a
// per-rule RuleFilter / FreezingArchRule baseline are NOT included until a rule
// that needs them is migrated into the standard set — see the M3 follow-up
// issues referenced in docs/architecture/202605281200-adr-cell-development-external-repo.md.
type ConfigForExternalCell struct {
	// ProductionMainPkgs lists the consumer's production composition-root
	// packages (the main packages that wire real adapters), as module-relative
	// import patterns (e.g. []string{"./cmd/myapp"}). Rules that scan the
	// composition root for forbidden in-memory/noop wiring read this; an empty
	// slice means "no composition-root scan for this run". GoCell's own dogfood
	// passes {"./cmd/corebundle"}.
	ProductionMainPkgs []string

	// ExtraRules are consumer-owned custom rules appended to the standard set —
	// the minimal plugin surface. They use the identical CellRule type (no
	// separate registration mechanism), mirroring ArchUnit's custom
	// DescribedPredicate/ArchCondition plugging into the same fluent API.
	ExtraRules []*CellRule
}

// StandardCellRules returns the platform's curated set of portable architecture
// invariants — the rules that apply to ANY repository building Cells on the
// GoCell platform (they reason about how the consumer uses platform APIs like
// errcode / panicregister, not about GoCell's own internal package layout).
//
// This is the GoCell analog of the []*analysis.Analyzer slice handed to
// multichecker.Main: a flat, registry-free list. The set grows as more rules
// are migrated from their legacy _test.go form into importable CellRules (M3
// PR-2..N, tracked in the umbrella ADR); the ratchet meta-archtest
// ARCHTEST-MODULE-PATH-FUNNEL-01 guarantees that migration converges.
func StandardCellRules() []*CellRule {
	return []*CellRule{
		{ID: rulePanicRegistered01, Run: CheckPanicRegistered},
	}
}

// RunStandardCellRules runs the curated [StandardCellRules] plus cfg.ExtraRules
// against the running module, reporting each rule's diagnostics through
// [Report]. It is the single entry point an external Cell repository calls:
//
//	func TestGoCellArchitecture(t *testing.T) {
//	    archtest.RunStandardCellRules(t, archtest.ConfigForExternalCell{
//	        ProductionMainPkgs: []string{"./cmd/myapp"},
//	    })
//	}
//
// The scan target is the consumer's own module (resolved by the drivers from
// its go.mod), so the same call works unchanged in GoCell and in an external
// repo. Each rule's failures surface independently via Report(t, rule.ID, …),
// so one failing rule does not mask another.
//
// GoCell itself does NOT need to call this in addition to its per-rule Tests:
// the per-rule Tests already dogfood the same Check* functions. This entry
// exists for external consumers and is exercised in-repo by
// TestRunStandardCellRules.
func RunStandardCellRules(t *testing.T, cfg ConfigForExternalCell) {
	t.Helper()
	rules := StandardCellRules()
	rules = append(rules, cfg.ExtraRules...)
	for _, r := range rules {
		if r == nil || r.Run == nil {
			continue
		}
		Report(t, r.ID, r.Run(t, cfg))
	}
}
