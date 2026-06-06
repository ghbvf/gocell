package archtest

import (
	"fmt"
	"testing"
)

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
//     by the running module (the typed Run drivers → findModuleRoot walk up from
//     the test process cwd, so they resolve the consumer's own go.mod), never
//     baked into the rule. ref: ArchUnit user guide; arch-go config.Load(modulePath).
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
// supplied by the driver (the Run typed scopes Typed/Production → findModuleRoot),
// which resolves the consumer's own go.mod for external repos.
const PlatformModulePath = "github.com/ghbvf/gocell"

// CellRule is a reusable, importable architecture invariant — the GoCell
// analog of golang.org/x/tools/go/analysis.Analyzer. ID is the stable rule
// identifier reported via [Report] (e.g. "PANIC-REGISTERED-01"); Run executes
// the rule against the module described by cfg and returns the diagnostics it
// observed (it does NOT call t.Errorf itself — [RunStandardCellRules] funnels
// every rule's diagnostics through [Report] uniformly).
//
// Run takes *testing.T because the underlying driver (Run with its AST / Typed /
// Production scopes) fails loud via t.Fatalf on load errors and needs the test
// handle. A rule whose scan is module-wide under the default build config may
// ignore cfg; rules that must see build-tagged files read cfg.BuildTags.
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
// Fields are deliberately minimal (YAGNI): every field is read by a rule that
// actually ships in [StandardCellRules]. A field reserved for a not-yet-migrated
// rule (e.g. a composition-root package list for PROD-MAIN-WIRING-NOOP-REJECT-01,
// issue #1303) is NOT added here until that rule lands — a public no-op field is
// a premature abstraction. ref: arch-go config.Load.
type ConfigForExternalCell struct {
	// BuildTags lists the consumer's production build tags (e.g.
	// []string{"prod", "amqp"}). Rules that must see code behind build
	// directives scan twice — once under the default build config and once with
	// these tags — so a violation hidden behind `//go:build prod` is not missed.
	// An empty slice means "scan the default build configuration only". GoCell's
	// own dogfood passes FlatNonDefaultTags() (its full non-default tag union);
	// an external repo passes whatever tags gate its production files.
	BuildTags []string

	// ExtraRules are consumer-owned custom rules appended to the standard set —
	// the minimal plugin surface. They use the identical CellRule type (no
	// separate registration mechanism), mirroring ArchUnit's custom
	// DescribedPredicate/ArchCondition plugging into the same fluent API.
	//
	// Trust boundary: each ExtraRule's Run receives the same *testing.T as the
	// standard rules. A rule that finds violations MUST return them as
	// []Diagnostic (funneled through Report → t.Errorf) and MUST NOT call
	// t.Fatal / t.FailNow itself — FailNow aborts the entire
	// RunStandardCellRules loop via runtime.Goexit, masking every standard rule
	// that would have run after it.
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
// PR-2..N, tracked at issue #1302); the ratchet meta-archtest
// ARCHTEST-MODULE-PATH-FUNNEL-01 guarantees that migration converges.
//
// Rules intentionally NOT registered (register=no) because they reason about
// GoCell's own internal package layout / source, not about how a consumer uses
// platform APIs — so they are vacuous-green or false-red in an external module:
//
//   - ERROR-FIRST-API-01 (CheckErrorFirstAPI01): gated by errorFirstEnforcedFiles,
//     a hardcoded 22-path positive allowlist of GoCell-specific files. An external
//     module has no files matching those paths → zero scan → vacuous green-pass
//     with no safety signal. The invariant is still enforced in GoCell itself via
//     TestErrorFirstAPI01.
//   - ERROR-FIRST-TYPED-NIL-01 (CheckErrorFirstTypedNil01): similarly gated by
//     errorFirstEnforcedFiles via errorFirstPackagePatterns(). Same false-safety
//     concern for external modules. Enforced in GoCell via TestErrorFirstTypedNil01.
//   - DETAILS-SEALED-FIELD-FROZEN-01 (CheckDetailsSealedFieldFrozen01): a
//     platform-source self-check — it reflects on errcode.PublicDetail/InternalDetail
//     and AST-parses pkg/errcode/details.go. The reflect half is tautological for a
//     consumer (it inspects the imported GoCell dependency type, which the consumer
//     cannot alter); the AST half resolves details.go under the CONSUMER module root
//     (findModuleRoot), where that file does not exist → false-red in a clean
//     external repo. It constrains GoCell's own errcode package shape, so it stays a
//     GoCell-internal self-check enforced via TestDetailsSealedFieldFrozen01.
//   - SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01 (CheckScaffoldListenerMarkerTypedConst):
//     scans the platform's own tools/codegen/cellgen package + the
//     templates/scaffold-cell.tmpl asset (neither exists in an external Cell repo)
//     → vacuous-green there. Migrated for the unified PlatformModulePath
//     parameterization + fork-safety only; enforced in GoCell via
//     TestScaffoldListenerMarkerTypedConst.
//   - SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01 (CheckSagaCoordinatorNoHeartbeatLoop):
//     reasons about GoCell-internal runtime/saga layout (or conformance enrollment)
//     → vacuous/false-red in an external module.
//   - SAGA-EXECUTOR-RAND-INJECTED-01 (CheckSagaExecutorRandInjected): reasons about
//     GoCell-internal runtime/saga layout (or conformance enrollment) → vacuous/
//     false-red in an external module.
//   - SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01 (CheckSagaJournalConformanceEnrollment):
//     reasons about GoCell-internal runtime/saga layout (or conformance enrollment)
//     → vacuous/false-red in an external module.
//   - SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01 (CheckSagaGlobalReaderConformanceEnrollment):
//     reasons about GoCell-internal runtime/saga layout (or conformance enrollment)
//     → vacuous/false-red in an external module.
//   - SAGA-JOURNAL-HOLDER-SEAL-01 (CheckSagaJournalHolderSeal): reasons about
//     GoCell-internal runtime/saga layout (or conformance enrollment) → vacuous/
//     false-red in an external module.
//   - SAGA-DRIVE-BEHIND-LEADER-GATE-01 (CheckSagaDriveBehindLeaderGate): reasons
//     about GoCell-internal runtime/saga layout (or conformance enrollment) →
//     vacuous/false-red in an external module.
//   - SAGA-STEP-RUN-OUTSIDE-TX-01 (CheckSagaStepRunOutsideTx): reasons about
//     GoCell-internal runtime/saga layout (or conformance enrollment) → vacuous/
//     false-red in an external module.
//   - SAGA-CONSTRUCTOR-NIL-GUARD-01 (CheckSagaConstructorNilGuard): reasons about
//     GoCell-internal runtime/saga layout (or conformance enrollment) → vacuous/
//     false-red in an external module.
//   - SAGA-SLOG-INSTANCE-FIELDS-CALLER-01 (CheckSagaSlogInstanceFieldsCaller):
//     reasons about GoCell-internal runtime/saga layout (or conformance enrollment)
//     → vacuous/false-red in an external module.
//   - SAGA-METRIC-LABEL-VALUES-FROZEN-01 (CheckSagaMetricLabelValuesFrozen):
//     reasons about GoCell-internal runtime/saga layout (or conformance enrollment)
//     → vacuous/false-red in an external module.
//
// An external consumer gets the rules migrated so far plus any cfg.ExtraRules
// they add. The set expands as additional portable rules land in #1302.
func StandardCellRules() []*CellRule {
	return []*CellRule{
		{ID: rulePanicRegistered01, Run: CheckPanicRegistered},
		{ID: ruleErrcodeKindLiteral01, Run: CheckErrcodeKindLiteralBanned},
		{ID: ruleMessageConstLiteral01, Run: CheckErrcodeMessageConstLiteral},
		{ID: ruleExportedErrorNew01, Run: CheckExportedErrorNew},
		// SCAFFOLD-DERIVED-FORCEOVERWRITE-01: bans consumer code from calling the
		// internal codegen primitive pathsafe.DerivedOverwrite outside the
		// sanctioned planDerivedArtifact site (no such site in an external repo →
		// pure ban). Cell-applicable; Hard downstream (types.Info caller-allowlist).
		{ID: ruleScaffoldDerivedForceOverwrite01, Run: CheckScaffoldDerivedForceOverwrite},
		{ID: sagaCompensatePureRuleID, Run: CheckSagaStepCompensatePure},
	}
}

// RunStandardCellRules runs the curated [StandardCellRules] plus cfg.ExtraRules
// against the running module, reporting each rule's diagnostics through
// [Report]. It is the single entry point an external Cell repository calls:
//
//	func TestGoCellArchitecture(t *testing.T) {
//	    archtest.RunStandardCellRules(t, archtest.ConfigForExternalCell{
//	        BuildTags: []string{"prod"},
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
	for i, r := range rules {
		if msg := validateCellRule(r, i); msg != "" {
			t.Errorf("RunStandardCellRules: %s", msg)
			continue
		}
		Report(t, r.ID, r.Run(t, cfg))
	}
}

// validateCellRule returns a non-empty message when rules[i] is misconfigured.
// A nil rule, a nil Run, or an empty ID is ALWAYS a consumer error (a rule
// slice assembled wrong) — never a benign no-op — so [RunStandardCellRules]
// reports it via t.Errorf and fails loud, rather than silently skipping and
// green-lighting a run that gated nothing. (An empty ID would also make
// Report(t, "", …) mis-attribute diagnostics to a blank rule, hiding which gate
// fired.) Extracted as a pure function so the rejection table is unit-testable
// without intercepting *testing.T.
func validateCellRule(r *CellRule, i int) string {
	switch {
	case r == nil:
		return fmt.Sprintf("rules[%d] is nil (remove it or supply a *CellRule)", i)
	case r.Run == nil:
		return fmt.Sprintf("rule %q has a nil Run (set CellRule.Run)", r.ID)
	case r.ID == "":
		return fmt.Sprintf("rules[%d] has an empty ID (set CellRule.ID)", i)
	}
	return ""
}
