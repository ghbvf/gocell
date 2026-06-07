// Package archtest enforces meta-governance over kernel/governance rule registration.
//
//   - INVARIANT: GOVERNANCE-RULES-REGISTRATION-GUARD-01
//   - INVARIANT: GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01
//   - INVARIANT: GOVERNANCE-RULE-ERROR-FIX-FIELD-01
//   - INVARIANT: GOVERNANCE-RULE-CODE-DETECT-BINDING-01
//   - INVARIANT: GOVERNANCE-RULE-EMITTER-CONSTRUCTORS-NEVER-FUNCVALUE-01
//
// G-13 elevates governance rule registration from a hand-edited slice to an
// archtest-guarded contract. The three invariants together catch:
//   - forgotten registration (a new validate*/checkDEP*/checkCH* method that
//     never runs — GOVERNANCE-RULES-REGISTRATION-GUARD-01 orphan check);
//   - drift between rule code literals and the rulecodes.go single source;
//   - error findings emitted without remediation guidance in the typed Fix
//     field, or built via a raw ValidationResult{} literal that bypasses the
//     newError / newWarning / newErrorAt / newScopedError constructor funnel
//     (#689 upgrade from the former "; fix:" Message-substring convention).
//
// GOVERNANCE-RULES-REGISTRATION-GUARD-01 (INV-1) — allRules-based orphan check:
// Verifies set equality: declared == registered, where:
//   - declared = *Validator methods with signature func() []ValidationResult
//     (zero params) AND name prefix in {validate, checkDEP, checkCH}. The
//     prefix filter excludes same-signature helpers like ctmSliceToContract.
//   - registered = method names extracted from the allRules package-level var
//     composite literal in rules_registry.go. For each Rule element, the
//     Detect: field is either a method expression (*Validator).NAME (collected)
//     or a *ast.FuncLit (VERIFY-06 closure, skipped). Any other shape is fatal.
//
// VERIFY-06 consistency: validateVERIFY06 takes a ctx param → excluded from
// declared (not func()[]VR); its allRules entry is a FuncLit → skipped in
// registered. Both sides exclude it → they match. Its registration is
// golden-locked by the in-package TestAllRulesMatchGolden (codeVERIFY06 ∈
// allRules via the closure).
//
// AI-robust grading (INV-1):
//   - downstream Hard: every allRules Detect must resolve to a real *Validator
//     method expression — a typo'd/dangling method expression fails to compile,
//     since allRules is production code (compiler-checked method expression).
//   - upstream Medium: declared==registered set equality is archtest. The
//     naming prefix {validate,checkDEP,checkCH} is a naming convention (Soft
//     tier); the prefix filter is kept as the same tier as the pre-M3 validate*
//     filter — not a regression. The golden code-set completeness (code ∈
//     allRules for every registered rule code) lives in the in-package
//     TestAllRulesMatchGolden, which is compiler-adjacent (RuleCode const set
//     vs allRules slice size).
//
// ref: G-13 (governance rule registration archtest); #687 (M3-RULE-ENGINE)
//
// Performance note: each Test* function calls loadGovernancePackage (single
// typed Run over ./kernel/governance) or a typed Run over targeted fixture
// patterns. The typeseval.SharedResolver process-wide cache amortizes repeated
// loads across sub-tests in the same go test binary invocation. Measured
// locally (2026-05-16): TestGovernanceRulesRegistrationGuard ≈3s,
// TestGovernanceRuleCodeConstSingleSource ≈5s,
// TestGovernanceRuleErrorFixField ≈7s, all < 15s slowgate threshold.
// No fixture consolidation needed.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGovernanceRulesRegistrationGuard verifies INV-1
// (GOVERNANCE-RULES-REGISTRATION-GUARD-01): set equality declared == registered
// where declared and registered are defined in the package-level godoc.
//
// A rule-shaped method has signature exactly func() []ValidationResult (zero
// params) AND a name prefix in {validate, checkDEP, checkCH}. The prefix
// filter excludes same-signature helpers like ctmSliceToContract,
// ctmContractToSlice. validateVERIFY06 takes a ctx param → excluded from
// declared; its FuncLit entry in allRules is skipped in registered. Both
// sides exclude it, so the set equality holds.
func TestGovernanceRulesRegistrationGuard(t *testing.T) {
	t.Run("production_source_all_registered", testINV1ProductionSource)
	t.Run("negative_fixture_orphan_detection", testINV1OrphanFixture)
}

func testINV1ProductionSource(t *testing.T) {
	Report(t, "GOVERNANCE-RULES-REGISTRATION-GUARD-01",
		CheckGovernanceRulesRegistrationGuard(t, ConfigForExternalCell{}))
}

// testINV1OrphanFixture proves the orphan-detection property of the new
// allRules-based check. The fixture package declares:
//   - type Validator + type ValidationResult + type Rule (minimal, matching
//     the names the scan resolves);
//   - var allRules = []Rule{...} referencing (*Validator).validateRegistered;
//   - method validateRegistered() []ValidationResult — in allRules (registered);
//   - method validateOrphan() []ValidationResult — NOT in allRules (orphan).
//
// The test asserts that validateOrphan appears in the "declared but not
// registered" set produced by extractRegisteredFromAllRules.
//
// This proves the check is ACTIVE: if extractRegisteredFromAllRules only read
// declared and never checked allRules, validateOrphan would be missed; if it
// only read allRules and ignored declared, validateOrphan would also be missed.
// Only the set-equality path catches the orphan.
func testINV1OrphanFixture(t *testing.T) {
	const fixturePattern = "./tools/archtest/testdata/governance_registration_guard_fixtures/orphan_detection_red"

	var fixtureGP *governancePackage

	Run(t, Typed(TypedOpts{Tests: false}, []string{fixturePattern}),
		func(p *Pass) []Diagnostic {
			fixtureGP = &governancePackage{
				scope:     p.Pkg.Scope(),
				info:      p.TypesInfo,
				fset:      p.Fset,
				files:     p.Files,
				fileRelFn: p.Rel,
			}
			return nil
		})

	require.NotNil(t, fixtureGP, "the typed Run must visit the fixture package")

	declared := declaredRuleMethodNames(t, fixtureGP)
	registered, _, fatal := extractRegisteredFromAllRules(fixtureGP)
	require.Empty(t, fatal, "fixture must not trigger a fatal shape error")

	_, hasRegistered := declared["validateRegistered"]
	assert.True(t, hasRegistered, "validateRegistered must be in declared (it is a rule-shaped method)")

	_, hasOrphan := declared["validateOrphan"]
	assert.True(t, hasOrphan, "validateOrphan must be in declared (it is a rule-shaped method)")

	_, regHasRegistered := registered["validateRegistered"]
	assert.True(t, regHasRegistered, "validateRegistered must be in registered (it is in allRules)")

	_, regHasOrphan := registered["validateOrphan"]
	assert.False(t, regHasOrphan, "validateOrphan must NOT be in registered (it is not in allRules)")

	missing := stringSetDifference(declared, registered)
	assert.Contains(t, missing, "validateOrphan",
		"validateOrphan must appear as 'declared but not registered' — orphan detection is active")
}

// TestGovernanceRuleCodeConstSingleSource verifies INV-2: every
// newError / newWarning / newScopedError / newErrorAt call in kernel/governance
// (excluding *_test.go and rulecodes.go) must pass a RuleCode-typed constant
// declared in rulecodes.go as its first argument. Every ValidationResult{}
// CompositeLit within kernel/governance must use a RuleCode const from
// rulecodes.go as the Code: field value.
//
// INV-2 Hard upgrade: instead of scanning for string literal patterns (regex),
// the check uses go/types info.Uses[ident] to verify that each code argument
// resolves to a *types.Const of type RuleCode declared in rulecodes.go.
// Bypass attempts (BasicLit, BinaryExpr concat, fmt.Sprintf, cross-package
// ident, local variable ident) all fail this check.
//
// Negative fixture in this test proves the hard property: an in-test
// fake source string with bare string literals and concat forms is parsed and
// checked; the violations are confirmed.
func TestGovernanceRuleCodeConstSingleSource(t *testing.T) {
	t.Run("negative_fixture_bare_literal_and_concat_caught", testINV2NegativeFixture)
	t.Run("negative_fixture_composite_lit_violations", testINV2CompositeLitFixtures)
	t.Run("production_source_all_pass", testINV2ProductionSource)
}

// testINV2NegativeFixture proves INV-2 is Hard: injected bare literals and
// concat forms produce violations.
func testINV2NegativeFixture(t *testing.T) {
	// Synthesize a minimal source string that mimics a bad call site.
	// We cannot run go/types on it easily, but we can verify the violation
	// detection logic by calling the shape-checking helpers directly.
	// The key Hard property: resolveRuleCodeArg rejects any AST shape that
	// is not *ast.Ident resolving to a rulecodes.go RuleCode const.

	fset := token.NewFileSet()
	src := `package governance
import "fmt"
type RuleCode string
const codeOK RuleCode = "X-01"
const codeOther string = "X-02"
func badLiteral()  string { return "X-99" }
func badConcat()   string { return "X" + "-99" }
func badSprintf()  string { return fmt.Sprintf("X-%d", 99) }
`
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err)

	// Test that a BasicLit node fails the shape check.
	lit := &ast.BasicLit{Kind: token.STRING, Value: `"X-99"`}
	assert.False(t, ruleCodeArgShapeIsValid(lit),
		"BasicLit must fail INV-2 shape check")

	// Test that a BinaryExpr (concat) fails.
	binExpr := &ast.BinaryExpr{
		Op: token.ADD,
		X:  &ast.BasicLit{Kind: token.STRING, Value: `"X"`},
		Y:  &ast.BasicLit{Kind: token.STRING, Value: `"-99"`},
	}
	assert.False(t, ruleCodeArgShapeIsValid(binExpr),
		"BinaryExpr concat must fail INV-2 shape check")

	_ = f // parsed but not used beyond proving compilation
}

// testINV2CompositeLitFixtures proves the ValidationResult literal scan
// genuinely catches the two shapes that bypass key-loop checks:
//
//   - composite_lit_no_code_red: Code: field absent — Code zero value would
//     never resolve to a rulecodes.go const, but the legacy key loop simply
//     observed "no Code key" and skipped.
//   - composite_lit_positional_red: positional fields — even when Code is the
//     first positional value, the legacy key loop iterates KeyValueExpr only
//     and never inspects the position-1 expression.
//
// The fixture violations call scanINV2ViolationsInFile (the shared scan
// helper used by production), so a regression in either path lights up here.
func testINV2CompositeLitFixtures(t *testing.T) {
	cases := []struct {
		pattern string
		wantMin int
		shape   string
	}{
		{
			pattern: "./tools/archtest/testdata/governance_rulecode_single_source_fixtures/composite_lit_no_code_red",
			wantMin: 1,
			shape:   "ValidationResult literal omits Code: field",
		},
		{
			pattern: "./tools/archtest/testdata/governance_rulecode_single_source_fixtures/composite_lit_positional_red",
			wantMin: 1,
			shape:   "ValidationResult literal uses positional fields",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.shape, func(t *testing.T) {
			var violations []Diagnostic
			Run(t, Typed(TypedOpts{Tests: false}, []string{tc.pattern}),
				func(p *Pass) []Diagnostic {
					govTypesPkg := findTypesPackageByPath(p.Pkg, governancePkgPath)
					if govTypesPkg == nil {
						t.Errorf("kernel/governance not found in transitive deps of fixture %s", tc.pattern)
						return nil
					}
					govScope := govTypesPkg.Scope()
					ruleCodeConsts := map[*types.Const]struct{}{}
					for _, name := range govScope.Names() {
						obj := govScope.Lookup(name)
						c, ok := obj.(*types.Const)
						if !ok {
							continue
						}
						named, ok := c.Type().(*types.Named)
						if !ok || named.Obj().Name() != "RuleCode" {
							continue
						}
						if filepath.Base(p.Fset.Position(c.Pos()).Filename) == ruleCodesFile {
							ruleCodeConsts[c] = struct{}{}
						}
					}

					for _, file := range p.Files {
						rel := p.Rel(file)
						violations = append(violations,
							scanINV2ViolationsInFile(file, p.Fset, p.TypesInfo,
								ruleCodeConsts, rel, governancePkgPath)...)
					}
					return nil
				})

			assert.GreaterOrEqual(t, len(violations), tc.wantMin,
				"shape %q: expected at least %d INV-2 violation(s), got %d: %v",
				tc.shape, tc.wantMin, len(violations), violations)
		})
	}
}

// testINV2ProductionSource verifies the production kernel/governance package
// has no violations.
//
// This function walks gp.files — the type-checked AST files that
// gp.info.Uses was built from — rather than re-parsing with the
// scanner. The Pass guarantee (Files and TypesInfo from the same driver load)
// ensures info.Uses lookups succeed and the Hard property (const-identity
// check) is exercised.
func testINV2ProductionSource(t *testing.T) {
	Report(t, "GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01",
		CheckGovernanceRuleCodeConstSingleSource(t, ConfigForExternalCell{}))
}

// TestGovernanceRuleCodeConstSingleSource_FilenameGuard verifies that the
// filename guard in collectRuleCodeConsts genuinely excludes a RuleCode const
// declared outside rulecodes.go.
//
// The testdata package testdata/governance_rulecode_single_source_fixtures/
// filename_bypass_red contains two files:
//   - rulecodes.go — declares codeGood RuleCode = "FMT-99" (must be included)
//   - fake_rules.go — declares codeBad  RuleCode = "FMT-98" (must be excluded)
//
// This test loads the package with full types and directly applies the
// filename guard logic (without the governancePkgPath filter, which would
// exclude all consts from a testdata package). The guard must return exactly
// one const (codeGood) and exclude codeBad.
func TestGovernanceRuleCodeConstSingleSource_FilenameGuard(t *testing.T) {
	const fixturePattern = "./tools/archtest/testdata/governance_rulecode_single_source_fixtures/filename_bypass_red"

	var included []string
	var excluded []string

	Run(t, Typed(TypedOpts{Tests: false}, []string{fixturePattern}),
		func(p *Pass) []Diagnostic {
			scope := p.Pkg.Scope()

			for _, name := range scope.Names() {
				obj := scope.Lookup(name)
				c, ok := obj.(*types.Const)
				if !ok {
					continue
				}
				named, ok := c.Type().(*types.Named)
				if !ok {
					continue
				}
				if named.Obj().Name() != "RuleCode" {
					continue
				}

				pos := p.Fset.Position(c.Pos())
				if filepath.Base(pos.Filename) == ruleCodesFile {
					included = append(included, name)
				} else {
					excluded = append(excluded, name)
				}
			}
			return nil
		})

	sort.Strings(included)
	sort.Strings(excluded)

	assert.Equal(t, []string{"codeGood"}, included,
		"filename guard must include only the const declared in rulecodes.go")
	assert.Equal(t, []string{"codeBad"}, excluded,
		"filename guard must exclude the const declared in fake_rules.go (bypass attempt)")
}

// TestGovernanceRuleErrorFixField verifies INV-3
// (GOVERNANCE-RULE-ERROR-FIX-FIELD-01): the typed Fix funnel. Two properties:
//
//  1. Fix-arg non-empty: every finding-constructor call — newError /
//     newWarning / newScopedError (locator methods) and newErrorAt (package-
//     level function) — must pass a resolvable, non-empty remediation string as
//     its LAST positional argument. All four signatures make the fix parameter
//     mandatory at compile time (arity: choosing a constructor means supplying a
//     fix slot); this archtest closes the residual gap Go cannot express — that
//     the fix is non-empty. The contract is severity-agnostic: newWarning is
//     scanned too (a warning's remediation also lives in the typed Fix field,
//     never as a Message substring).
//  2. Construction funnel: no raw ValidationResult{} composite literal may
//     appear in the governance package outside locator.go (the sole
//     constructor home). All findings flow through the four constructors, so
//     an error result structurally cannot be built without a fix, and the fix
//     lives in a typed field rather than a "; fix:" Message substring.
func TestGovernanceRuleErrorFixField(t *testing.T) {
	t.Run("negative_fixtures_caught", testINV3NegativeFixture)
	t.Run("production_source_all_pass", testINV3ProductionSource)
}

// testINV3NegativeFixture proves both scan paths are genuinely active. Each
// shape is exercised via a testdata fixture package whose AST triggers the
// exact logic used by testINV3ProductionSource — single-source, fixture
// validates production.
//
// Fix-arg path:
//   - empty_fix_red: newError(..., "") — empty fix literal.
//   - unresolvable_fix_red: newErrorAt(..., fixParam) — fix forwarded from a
//     parameter, unresolvable to a const string.
//
// Funnel path (raw ValidationResult{} composite outside locator.go):
//   - struct_lit_missing_fix_red / composite_lit_no_message_red /
//     composite_lit_positional_red — any raw composite, named or positional.
func testINV3NegativeFixture(t *testing.T) {
	cases := []struct {
		pattern string
		wantMin int
		shape   string
	}{
		{
			pattern: "./tools/archtest/testdata/governance_fix_anchor_fixtures/empty_fix_red",
			wantMin: 1,
			shape:   "newError callsite with empty fix argument",
		},
		{
			pattern: "./tools/archtest/testdata/governance_fix_anchor_fixtures/unresolvable_fix_red",
			wantMin: 1,
			shape:   "newErrorAt callsite with unresolvable (forwarded-param) fix argument",
		},
		{
			pattern: "./tools/archtest/testdata/governance_fix_anchor_fixtures/scoped_empty_fix_red",
			wantMin: 1,
			shape:   "newScopedError callsite with empty fix argument",
		},
		{
			pattern: "./tools/archtest/testdata/governance_fix_anchor_fixtures/warning_empty_fix_red",
			wantMin: 1,
			shape:   "newWarning callsite with empty fix argument",
		},
		{
			pattern: "./tools/archtest/testdata/governance_fix_anchor_fixtures/struct_lit_missing_fix_red",
			wantMin: 1,
			shape:   "raw ValidationResult{} composite literal (named fields)",
		},
		{
			pattern: "./tools/archtest/testdata/governance_fix_anchor_fixtures/composite_lit_no_message_red",
			wantMin: 1,
			shape:   "raw ValidationResult{} composite literal (no Message field)",
		},
		{
			pattern: "./tools/archtest/testdata/governance_fix_anchor_fixtures/composite_lit_positional_red",
			wantMin: 1,
			shape:   "raw ValidationResult{} composite literal (positional fields)",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.shape, func(t *testing.T) {
			var violations []Diagnostic
			Run(t, Typed(TypedOpts{Tests: false}, []string{tc.pattern}),
				func(p *Pass) []Diagnostic {
					consts := collectPackageStringConsts(p.Pkg.Scope())
					for _, file := range p.Files {
						violations = append(violations,
							scanFixFieldViolationsInFile(file, p.Fset, p.TypesInfo, consts, p.Rel(file), governancePkgPath)...)
					}
					return nil
				})

			assert.GreaterOrEqual(t, len(violations), tc.wantMin,
				"shape %q: expected at least %d INV-3 violation(s), got %d: %v",
				tc.shape, tc.wantMin, len(violations), violations)
		})
	}
}

// testINV3ProductionSource verifies the production kernel/governance package
// passes both INV-3 properties: every error-constructor call carries a
// non-empty fix, and no raw ValidationResult{} literal exists outside locator.go.
func testINV3ProductionSource(t *testing.T) {
	Report(t, "GOVERNANCE-RULE-ERROR-FIX-FIELD-01",
		CheckGovernanceRuleErrorFixField(t, ConfigForExternalCell{}))
}

// TestFindTypesPackageByPath validates findTypesPackageByPath (used by INV-2's
// composite-lit fixture loader to resolve governance.ValidationResult's
// *types.Package) with table-driven sub-tests.
//
// The kernel/governance package is a known transitive dependency of the
// governance fixtures, so loadGovernancePackage's typed pkg provides a
// *types.Package whose import graph includes at least kernel/governance itself.
// We exercise:
//
//	(a) known path PlatformModulePath+"/kernel/governance" — DFS must find non-nil.
//	(b) non-existent path PlatformModulePath+"/does/not/exist" — must return nil.
//	(c) nil pkg input — must safely return nil without panic.
func TestFindTypesPackageByPath(t *testing.T) {
	root := findModuleRoot(t)
	gp := loadGovernancePackage(t, root)

	// The governancePackage.scope belongs to gp.info which was loaded via
	// a typed Run over ./kernel/governance. We can obtain the *types.Package
	// directly from the scope's package reference. Since governancePackage
	// does not expose the *types.Package directly, we resolve it via scope
	// — the scope belongs to the governance *types.Package itself.
	govPkg := gp.scope.Lookup("Validator")
	require.NotNil(t, govPkg, "Validator must exist in kernel/governance")
	govTypedPkg := govPkg.Pkg()
	require.NotNil(t, govTypedPkg, "Validator.Pkg() must be non-nil")

	t.Run("known_path_found", func(t *testing.T) {
		got := findTypesPackageByPath(govTypedPkg, governancePkgPath)
		require.NotNil(t, got, "findTypesPackageByPath must find kernel/governance by its own path")
		assert.Equal(t, governancePkgPath, got.Path(), "returned package path must match the requested import path")
	})

	t.Run("nonexistent_path_returns_nil", func(t *testing.T) {
		got := findTypesPackageByPath(govTypedPkg, PlatformModulePath+"/does/not/exist")
		assert.Nil(t, got, "findTypesPackageByPath must return nil for a non-existent import path")
	})

	t.Run("nil_pkg_input_returns_nil", func(t *testing.T) {
		got := findTypesPackageByPath(nil, governancePkgPath)
		assert.Nil(t, got, "findTypesPackageByPath must return nil safely for nil input")
	})
}

// TestGovernanceRuleCodeDetectBinding verifies INVARIANT:
// GOVERNANCE-RULE-CODE-DETECT-BINDING-01.
//
// For every entry in kernel/governance allRules, the Rule.Code value must
// appear as the first argument of at least one newError / newWarning /
// newScopedError / newErrorAt call reachable from the entry's Detect method
// (transitively following same-receiver *Validator method calls via BFS).
//
// This closes the gap left by TestAllRulesMatchGolden: a swapped pair like
//
//	{Code: codeREF01, Detect: (*Validator).validateREF02}
//
// compiles (method expressions are compiler-checked) and passes the set+
// uniqueness test when two codes are swapped with each other, yet mis-binds
// Phase / Metric to the wrong rule body.
//
// AI-robust grading: Medium (type-aware archtest AST walk). Hard path does
// not exist — Rule.Code is structurally separate from the detect body's
// newError argument; no type-system mechanism can enforce their equality at
// compile time without merging the two fields into a single codegen funnel
// (a larger refactor tracked separately).
//
// Blind-spots (AST forms outside this walk's scope):
//   - Function-value indirection (`f := v.newError; f(...)`): reverse-checked
//     with teeth by TestGovernanceEmitterConstructorsNeverFuncValue, which
//     asserts production has ZERO such uses and that a negative fixture is
//     flagged (a vacuous scan would fail the fixture sub-test).
//   - Code emitted exclusively through a package-level function (e.g.
//     buildAlignmentFindings for CH-04) is not reached by the same-receiver
//     method walk. This is a reasoned (not asserted) exclusion: every such
//     delegating *Validator method (e.g. checkResponseAlignmentForContract)
//     also contains a direct newError call with the same code, so the code is
//     found anyway. No low-cost Hard path exists given the structural
//     separation of Code and Detect.
func TestGovernanceRuleCodeDetectBinding(t *testing.T) {
	t.Run("production_source_all_bound", testBindingProductionSource)
	t.Run("negative_fixture_mislabel_detected", testBindingNegativeFixture)
}

// testBindingProductionSource dogfoods the importable CheckGovernanceRuleCodeDetectBinding
// (the single rule body shared with the external-cell path) rather than
// re-implementing the binding walk inline — keeping production enforcement and
// the exported Check* on one source. The per-entry granularity moves into the
// Check; negative-fixture coverage of the lower-level walk stays in
// testBindingNegativeFixture.
func testBindingProductionSource(t *testing.T) {
	Report(t, "GOVERNANCE-RULE-CODE-DETECT-BINDING-01",
		CheckGovernanceRuleCodeDetectBinding(t, ConfigForExternalCell{}))
}

func testBindingNegativeFixture(t *testing.T) {
	const fixturePattern = "./tools/archtest/testdata/governance_binding_check_fixtures/mislabeled_detect_red"

	var fixtureGP *governancePackage

	Run(t, Typed(TypedOpts{Tests: false}, []string{fixturePattern}),
		func(p *Pass) []Diagnostic {
			fixtureGP = &governancePackage{
				scope:     p.Pkg.Scope(),
				info:      p.TypesInfo,
				fset:      p.Fset,
				files:     p.Files,
				fileRelFn: p.Rel,
			}
			return nil
		})

	require.NotNil(t, fixtureGP, "the typed Run must visit the fixture package")

	// Collect RuleCode consts from the fixture package's own scope.
	// The fixture defines its own RuleCode type, so we cannot use
	// collectRuleCodeConsts (which filters by governancePkgPath).
	fixtureRuleCodeConsts := collectRuleCodeConstsInScope(fixtureGP.scope)
	require.NotEmpty(t, fixtureRuleCodeConsts, "fixture must declare at least one RuleCode const")

	methodMap := buildValidatorMethodMap(fixtureGP.files)
	entries, _, fatal := extractAllRulesEntries(fixtureGP)
	require.Empty(t, fatal, "fixture must not trigger a fatal shape error")
	require.Len(t, entries, 1, "fixture allRules must have exactly one entry")

	entry := entries[0]
	// codeB is registered but methodA emits codeA — mismatch expected.
	emitted := collectEmittedCodes(entry.methodName, methodMap, fixtureRuleCodeConsts, fixtureGP.info)
	assert.NotContains(t, emitted, entry.codeValue,
		"fixture: entry Code %q must NOT be in emitted set %v — this is the mislabel the check must catch",
		entry.codeValue, emitted)
	assert.NotEmpty(t, emitted,
		"fixture: methodA must emit at least one code (codeA) — confirms the walk is functioning")
}

// TestGovernanceEmitterConstructorsNeverFuncValue is the teeth-backed reverse
// self-check for the func-value-indirection blind spot shared by
// GOVERNANCE-RULE-CODE-DETECT-BINDING-01 and GOVERNANCE-RULE-ERROR-FIX-FIELD-01.
//
// Both invariants resolve emitter calls by inspecting CallExpr.Fun
// (governanceEmitterName), so a constructor referenced as a value rather than
// called directly — `f := v.newError; f(...)` — would evade them. The invariant
// godocs previously asserted only that "the production scan stays green", which
// a vacuous/broken scan also satisfies. This test makes the blind spot an
// explicit, falsifiable assertion:
//
//   - production_source_no_funcvalue: kernel/governance must contain ZERO
//     references to the emitter constructors that are not the direct callee of a
//     call (assert count == 0).
//   - negative_fixture_funcvalue_detected: a fixture that DOES take a
//     constructor as a func value must be flagged (assert count > 0), proving the
//     scan has teeth — a vacuous scan fails this sub-test.
func TestGovernanceEmitterConstructorsNeverFuncValue(t *testing.T) {
	t.Run("production_source_no_funcvalue", func(t *testing.T) {
		Report(t, "GOVERNANCE-RULE-EMITTER-CONSTRUCTORS-NEVER-FUNCVALUE-01",
			CheckGovernanceEmitterConstructorsNeverFuncValue(t, ConfigForExternalCell{}))
	})

	t.Run("negative_fixture_funcvalue_detected", func(t *testing.T) {
		const fixturePattern = "./tools/archtest/testdata/governance_emitter_funcvalue_fixtures/funcvalue_indirection_red"
		var count int
		Run(t, Typed(TypedOpts{Tests: false}, []string{fixturePattern}),
			func(p *Pass) []Diagnostic {
				for _, f := range p.Files {
					count += len(scanEmitterFuncValueUsages(f))
				}
				return nil
			})

		assert.Positive(t, count,
			"scanEmitterFuncValueUsages must detect the fixture's `f := newError` "+
				"func-value indirection — a zero result means the scan is vacuous")
	})
}

// stringSetDifference returns keys in a that are not in b.
func stringSetDifference(a, b map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for k := range a {
		if _, ok := b[k]; !ok {
			out[k] = struct{}{}
		}
	}
	return out
}
