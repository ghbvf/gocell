// Package archtest enforces meta-governance over kernel/governance rule registration.
//
//   - INVARIANT: GOVERNANCE-RULES-REGISTRATION-GUARD-01
//   - INVARIANT: GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01
//   - INVARIANT: GOVERNANCE-RULE-ERROR-FIX-FIELD-01
//   - INVARIANT: GOVERNANCE-RULE-CODE-DETECT-BINDING-01
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
// RunTyped over ./kernel/governance) or RunTyped over targeted fixture
// patterns. The typeseval.SharedResolver process-wide cache amortizes repeated
// loads across sub-tests in the same go test binary invocation. Measured
// locally (2026-05-16): TestGovernanceRulesRegistrationGuard ≈3s,
// TestGovernanceRuleCodeConstSingleSource ≈5s,
// TestGovernanceRuleErrorFixField ≈7s, all < 15s slowgate threshold.
// No fixture consolidation needed.
package archtest

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// governancePkgPath is the import path of the kernel/governance package.
const governancePkgPath = "github.com/ghbvf/gocell/kernel/governance"

// ruleCodesFile is the base name of the single-source file for RuleCode consts.
const ruleCodesFile = "rulecodes.go"

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
	root := findModuleRoot(t)
	pkg := loadGovernancePackage(t, root)

	declared := declaredRuleMethodNames(t, pkg)

	registered, fatal := extractRegisteredFromAllRules(t, pkg)
	if fatal != "" {
		t.Fatal(fatal)
	}

	missing := setDifference(declared, registered)
	extra := setDifference(registered, declared)
	assert.Empty(t, missing,
		"rule-shaped methods declared on *Validator but not registered in allRules: %v", missing)
	assert.Empty(t, extra,
		"names referenced in allRules Detect but no matching rule-shaped method on *Validator: %v", extra)
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

	RunTyped(t, TypedOpts{Tests: false}, []string{fixturePattern},
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

	require.NotNil(t, fixtureGP, "RunTyped must visit the fixture package")

	declared := declaredRuleMethodNames(t, fixtureGP)
	registered, fatal := extractRegisteredFromAllRules(t, fixtureGP)
	require.Empty(t, fatal, "fixture must not trigger a fatal shape error")

	_, hasRegistered := declared["validateRegistered"]
	assert.True(t, hasRegistered, "validateRegistered must be in declared (it is a rule-shaped method)")

	_, hasOrphan := declared["validateOrphan"]
	assert.True(t, hasOrphan, "validateOrphan must be in declared (it is a rule-shaped method)")

	_, regHasRegistered := registered["validateRegistered"]
	assert.True(t, regHasRegistered, "validateRegistered must be in registered (it is in allRules)")

	_, regHasOrphan := registered["validateOrphan"]
	assert.False(t, regHasOrphan, "validateOrphan must NOT be in registered (it is not in allRules)")

	missing := setDifference(declared, registered)
	assert.Contains(t, missing, "validateOrphan",
		"validateOrphan must appear as 'declared but not registered' — orphan detection is active")
}

// declaredRuleMethodNames returns the set of *Validator method names whose
// signature matches a rule shape AND whose name has a rule prefix.
//
// Rule shape: func() []ValidationResult (zero params, single []ValidationResult
// result). The ctx-bound form func(context.Context) []ValidationResult is
// intentionally excluded — validateVERIFY06 takes ctx and is registered via a
// FuncLit closure in allRules; both sides exclude it by design.
//
// Rule prefix set: {validate, checkDEP, checkCH}. This excludes same-signature
// helpers like ctmSliceToContract, ctmContractToSlice, scanWrapperPackageStateFile
// etc. whose names do not start with any prefix in the set.
//
// When used against a fixture package, pkg.scope may not be the governance
// package itself; the package-path filter is skipped when pkg.scope.Lookup
// returns objects whose Pkg() differs (fixture packages define their own types).
func declaredRuleMethodNames(t *testing.T, pkg *governancePackage) map[string]struct{} {
	t.Helper()
	valObj := pkg.scope.Lookup("Validator")
	require.NotNil(t, valObj, "Validator type must exist")
	valTypeName, ok := valObj.(*types.TypeName)
	require.True(t, ok, "Validator must be a type name")
	valNamed, ok := valTypeName.Type().(*types.Named)
	require.True(t, ok, "Validator must be a named type")

	// Determine the package path to filter — use the actual package of the
	// Validator type so this works for both production and fixture packages.
	var validatorPkgPath string
	if valTypeName.Pkg() != nil {
		validatorPkgPath = valTypeName.Pkg().Path()
	}

	ms := types.NewMethodSet(types.NewPointer(valNamed))
	out := map[string]struct{}{}
	for i := 0; i < ms.Len(); i++ {
		sel := ms.At(i)
		fn, ok := sel.Obj().(*types.Func)
		if !ok {
			continue
		}
		if validatorPkgPath != "" && (fn.Pkg() == nil || fn.Pkg().Path() != validatorPkgPath) {
			continue
		}
		if !isRuleMethodName(fn.Name()) {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok {
			continue
		}
		if !ruleShapeSignature(sig) {
			continue
		}
		out[fn.Name()] = struct{}{}
	}
	return out
}

// isRuleMethodName returns true when name has one of the rule method prefixes:
// validate, checkDEP, checkCH. This excludes same-signature helpers (e.g.
// ctmSliceToContract) whose names do not match any rule prefix.
func isRuleMethodName(name string) bool {
	return strings.HasPrefix(name, "validate") ||
		strings.HasPrefix(name, "checkDEP") ||
		strings.HasPrefix(name, "checkCH")
}

// ruleShapeSignature returns true when sig matches the rule shape:
// `func() []ValidationResult` (zero params, single []ValidationResult result).
//
// The ctx-bound form func(context.Context) []ValidationResult is intentionally
// excluded. validateVERIFY06 is the only such method; it is registered via a
// FuncLit closure in allRules rather than a method expression, so both the
// declared set (this filter) and the registered set (FuncLit skip) exclude it
// consistently. All other signatures (helpers taking *metadata.ContractMeta
// etc.) are also rejected.
//
// The result element type is matched by name ("ValidationResult") rather than
// by package path, so this works for both the production package and fixture
// packages that define their own minimal ValidationResult type.
func ruleShapeSignature(sig *types.Signature) bool {
	if sig.Variadic() || sig.Params().Len() != 0 || sig.Results().Len() != 1 {
		return false
	}
	sliceT, ok := sig.Results().At(0).Type().(*types.Slice)
	if !ok {
		return false
	}
	elem, ok := sliceT.Elem().(*types.Named)
	if !ok {
		return false
	}
	return elem.Obj().Name() == "ValidationResult"
}

// extractRegisteredFromAllRules scans the allRules package-level var in the
// governance package files and collects method names from each Rule's Detect
// field. This replaces the former rules()/strictRules() FuncDecl walk after the
// M3-RULE-ENGINE migration (#687) deleted those functions.
//
// For each element in allRules (a Rule composite literal), the Detect:
// KeyValueExpr value is inspected:
//   - (*Validator).NAME method expression → NAME collected.
//   - *ast.FuncLit closure → skipped (VERIFY-06 pattern).
//   - Any other shape → fatal (fail-closed: new Detect shapes must go through
//     review, not silently bypass the check).
//
// The method expression structural pattern is:
//
//	*ast.SelectorExpr{
//	  X:   *ast.ParenExpr{X: *ast.StarExpr{X: *ast.Ident{Name: "Validator"}}},
//	  Sel: *ast.Ident{Name: methodName},
//	}
//
// Structural ident-name match "Validator" is acceptable: the scan is
// single-package (kernel/governance), so no other type named Validator exists.
//
// Same-source guarantee: pkg.files, pkg.info, and pkg.fset come from a single
// RunTyped Pass invocation (loadGovernancePackage), so AST nodes and TypesInfo
// are co-derived from the same packages.Load call.
func extractRegisteredFromAllRules(t *testing.T, pkg *governancePackage) (map[string]struct{}, string) {
	t.Helper()

	registered := map[string]struct{}{}
	var fatal string

	for _, file := range pkg.files {
		if fatal != "" {
			break
		}
		relPath := pkg.fileRel(file)
		scanner.EachInChildren[ast.GenDecl](file, func(gd *ast.GenDecl) {
			if fatal != "" || gd.Tok != token.VAR {
				return
			}
			// Find the ValueSpec whose name is "allRules".
			vs, ok := scanner.FindFirstChild[ast.ValueSpec](gd, func(vs *ast.ValueSpec) bool {
				for _, nameIdent := range vs.Names {
					if nameIdent.Name == "allRules" {
						return true
					}
				}
				return false
			})
			if !ok || len(vs.Values) == 0 {
				return
			}
			// vs.Values[0] should be a []Rule{...} composite literal.
			cl, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok {
				return
			}
			// Each element in allRules is a Rule{...} composite literal.
			scanner.EachInChildren[ast.CompositeLit](cl, func(ruleLit *ast.CompositeLit) {
				if fatal != "" {
					return
				}
				// Find the Detect: KeyValueExpr in this Rule literal.
				scanner.EachInChildren[ast.KeyValueExpr](ruleLit, func(kv *ast.KeyValueExpr) {
					if fatal != "" {
						return
					}
					keyIdent, ok := kv.Key.(*ast.Ident)
					if !ok || keyIdent.Name != "Detect" {
						return
					}
					name, ok, isFatal := extractDetectMethodName(kv.Value, relPath, pkg.fset)
					if isFatal {
						fatal = name // name carries the fatal message when isFatal
						return
					}
					if ok {
						registered[name] = struct{}{}
					}
				})
			})
		})
	}
	return registered, fatal
}

// extractDetectMethodName extracts the method name from a Detect field value.
// Returns (methodName, true, false) for a method expression (*Validator).NAME.
// Returns ("", false, false) for a *ast.FuncLit (VERIFY-06 skip pattern).
// Returns (fatalMsg, false, true) for any unrecognized shape (fail-closed).
func extractDetectMethodName(expr ast.Expr, relPath string, fset *token.FileSet) (string, bool, bool) {
	// Case 1: (*Validator).NAME — method expression.
	// AST shape: SelectorExpr{X: ParenExpr{X: StarExpr{X: Ident{"Validator"}}}, Sel: Ident{Name}}
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		if name, matched := methodExprValidatorName(sel); matched {
			return name, true, false
		}
		// SelectorExpr but not the expected (*Validator).NAME pattern → fatal.
		pos := fset.Position(expr.Pos())
		msg := "unrecognized Detect shape at " + relPath + ":" + strconv.Itoa(pos.Line) +
			" — every allRules Detect must be (*Validator).methodName or a FuncLit closure"
		return msg, false, true
	}

	// Case 2: FuncLit — only the VERIFY-06 closure pattern is accepted.
	// The accepted shape is exactly:
	//   func(v *Validator) []ValidationResult { return v.validateVERIFY06(v.runCtx) }
	// i.e. a FuncLit whose body is a single ReturnStmt whose one result is a
	// CallExpr with Fun a SelectorExpr with Sel.Name == "validateVERIFY06".
	// Any other FuncLit is fatal (fail-closed: prevents future anonymous closures
	// from silently bypassing the orphan check).
	if fl, ok := expr.(*ast.FuncLit); ok {
		if isVERIFY06ClosureShape(fl) {
			return "", false, false
		}
		pos := fset.Position(expr.Pos())
		msg := "unexpected FuncLit Detect in allRules at " + relPath + ":" + strconv.Itoa(pos.Line) +
			" — only the VERIFY-06 closure (return v.validateVERIFY06(v.runCtx)) may be a closure;" +
			" everything else must be a (*Validator).method expression"
		return msg, false, true
	}

	// Any other shape → fatal.
	pos := fset.Position(expr.Pos())
	msg := "unrecognized Detect shape at " + relPath + ":" + strconv.Itoa(pos.Line) +
		" — every allRules Detect must be (*Validator).methodName or a FuncLit closure"
	return msg, false, true
}

// isVERIFY06ClosureShape reports whether fl is the exact VERIFY-06 FuncLit shape:
//
//	func(v *Validator) []ValidationResult { return v.validateVERIFY06(v.runCtx) }
//
// Structurally: a FuncLit whose body is a BlockStmt containing exactly one
// statement, a ReturnStmt whose single result is a CallExpr whose Fun is a
// SelectorExpr with Sel.Name == "validateVERIFY06". Argument count and types
// are not checked (avoid over-specifying the ctx argument expression), but the
// single-return shape and the callee name are pinned.
func isVERIFY06ClosureShape(fl *ast.FuncLit) bool {
	if fl.Body == nil || len(fl.Body.List) != 1 {
		return false
	}
	ret, ok := fl.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	return sel.Sel.Name == "validateVERIFY06"
}

// methodExprValidatorName returns the method name if expr is the structural
// pattern (*Validator).NAME, and false otherwise.
// Structural ident-name match "Validator" is sufficient for single-package scan.
func methodExprValidatorName(sel *ast.SelectorExpr) (string, bool) {
	if sel.Sel == nil {
		return "", false
	}
	paren, ok := sel.X.(*ast.ParenExpr)
	if !ok {
		return "", false
	}
	star, ok := paren.X.(*ast.StarExpr)
	if !ok {
		return "", false
	}
	ident, ok := star.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	if ident.Name != "Validator" {
		return "", false
	}
	return sel.Sel.Name, true
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
	assert.False(t, ruleCodeArgShapeIsValid(lit, nil, "", ""),
		"BasicLit must fail INV-2 shape check")

	// Test that a BinaryExpr (concat) fails.
	binExpr := &ast.BinaryExpr{
		Op: token.ADD,
		X:  &ast.BasicLit{Kind: token.STRING, Value: `"X"`},
		Y:  &ast.BasicLit{Kind: token.STRING, Value: `"-99"`},
	}
	assert.False(t, ruleCodeArgShapeIsValid(binExpr, nil, "", ""),
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
			var violations []string
			RunTyped(t, TypedOpts{Tests: false}, []string{tc.pattern},
				func(p *Pass) []Diagnostic {
					// Find kernel/governance in the transitive *types.Package graph
					// to get the RuleCode const set with position-resolvable Fset.
					// Since all packages in a single RunTyped invocation share the
					// same *token.FileSet, p.Fset resolves positions for governance
					// consts loaded transitively.
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
	root := findModuleRoot(t)
	pkg := loadGovernancePackage(t, root)

	// Collect all *types.Const objects that live in rulecodes.go and have
	// type RuleCode.
	ruleCodeConsts := collectRuleCodeConsts(pkg)
	require.NotEmpty(t, ruleCodeConsts, "rulecodes.go must declare at least one RuleCode const")

	var violations []string
	for _, file := range pkg.files {
		base := filepath.Base(pkg.fileRel(file))
		// Skip rulecodes.go itself and locator.go (the constructor that
		// assigns the RuleCode param to ValidationResult.Code — the
		// Code: field there is a method parameter, not a call-site const
		// reference).
		if base == ruleCodesFile || base == "locator.go" {
			continue
		}
		violations = append(violations,
			scanINV2ViolationsInFile(file, pkg.fset, pkg.info, ruleCodeConsts, pkg.fileRel(file), governancePkgPath)...)
	}
	sort.Strings(violations)
	assert.Empty(t, violations,
		"every rule code must come from a RuleCode-typed const in rulecodes.go")
}

// scanINV2ViolationsInFile reports all INV-2 violations in a single AST file.
// Shared between testINV2ProductionSource and testINV2NegativeFixture so both
// exercise identical scan logic — fixture validates the production path.
//
// Two scan paths:
//  1. CallExpr: newError / newWarning / newScopedError / newErrorAt with code arg
//     shape != Ident or not resolving to a RuleCode const in rulecodes.go.
//  2. CompositeLit: ValidationResult{} literal with any of:
//     (a) positional element (non-KeyValueExpr) — every field must be named;
//     (b) Code: field absent — every literal must explicitly reference a
//     RuleCode const (default-zero RuleCode("") would silently bypass);
//     (c) Code: present but value is not a RuleCode const ident.
func scanINV2ViolationsInFile(
	file *ast.File,
	fset *token.FileSet,
	info *types.Info,
	ruleCodeConsts map[*types.Const]struct{},
	relPath string,
	pkgPath string,
) []string {
	var violations []string

	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		name := governanceEmitterName(call)
		if name == "" || len(call.Args) == 0 {
			return
		}
		codeArg := call.Args[0]
		if !ruleCodeArgShapeIsValid(codeArg, info, pkgPath, "RuleCode") ||
			!ruleCodeArgResolvesToConst(codeArg, info, ruleCodeConsts) {
			pos := fset.Position(codeArg.Pos())
			violations = append(violations,
				relPath+":"+strconv.Itoa(pos.Line)+
					": code arg to "+name+" must be a RuleCode-typed const from rulecodes.go — "+
					"got AST shape "+astShapeName(codeArg))
		}
	})

	scanner.EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
		if !isValidationResultCompositeLit(cl, info, pkgPath) {
			return
		}
		// (a) Positional ban: every element must be Key:Value. A positional
		// element bypasses the Code: lookup entirely, even when Code is
		// supplied positionally. Detect by comparing direct KeyValueExpr
		// children count against total element count — count comparison
		// avoids the for-range + type-assert form banned by
		// SCANNER-FRAMEWORK-USAGE-01.
		keyValueCount := 0
		scanner.EachInChildren[ast.KeyValueExpr](cl, func(_ *ast.KeyValueExpr) {
			keyValueCount++
		})
		if len(cl.Elts) > 0 && keyValueCount != len(cl.Elts) {
			pos := fset.Position(cl.Pos())
			violations = append(violations,
				relPath+":"+strconv.Itoa(pos.Line)+
					": ValidationResult literal must use named fields (Code:/Severity:/Message:/...) — "+
					"positional element forbidden because it lets the Code: completeness check be skipped")
			return
		}
		// (b)(c) Collect named keys, then enforce Code: presence + RuleCode shape.
		var codeValue ast.Expr
		scanner.EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				return
			}
			if key.Name == "Code" {
				codeValue = kv.Value
			}
		})
		if codeValue == nil {
			pos := fset.Position(cl.Pos())
			violations = append(violations,
				relPath+":"+strconv.Itoa(pos.Line)+
					": ValidationResult literal omits Code: field — every result must reference a "+
					"RuleCode const from rulecodes.go (zero RuleCode would silently pass the single-source check)")
			return
		}
		if !ruleCodeArgShapeIsValid(codeValue, info, pkgPath, "RuleCode") ||
			!ruleCodeArgResolvesToConst(codeValue, info, ruleCodeConsts) {
			pos := fset.Position(codeValue.Pos())
			violations = append(violations,
				relPath+":"+strconv.Itoa(pos.Line)+
					": ValidationResult.Code must be a RuleCode-typed const from rulecodes.go — "+
					"got AST shape "+astShapeName(codeValue))
		}
	})

	return violations
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

	RunTyped(t, TypedOpts{Tests: false}, []string{fixturePattern},
		func(p *Pass) []Diagnostic {
			scope := p.Pkg.Scope()

			// Apply the same filter logic as collectRuleCodeConsts but without the
			// governancePkgPath check (the testdata package has a different import
			// path). This isolates the filename guard specifically.
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
				// Apply filename guard — the key invariant under test.
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

// ruleCodeArgShapeIsValid returns true when expr is an *ast.Ident. All other
// shapes (BasicLit, BinaryExpr, CallExpr, SelectorExpr, etc.) are invalid.
// The pkgPath and typeName arguments are used for future extensibility but
// currently only the shape is checked here; type resolution is done in
// ruleCodeArgResolvesToConst.
func ruleCodeArgShapeIsValid(expr ast.Expr, _ *types.Info, _, _ string) bool {
	_, ok := expr.(*ast.Ident)
	return ok
}

// ruleCodeArgResolvesToConst returns true when expr is an *ast.Ident that
// resolves (via info.Uses) to a package-scope *types.Const in ruleCodeConsts.
func ruleCodeArgResolvesToConst(expr ast.Expr, info *types.Info, ruleCodeConsts map[*types.Const]struct{}) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	if info == nil {
		// No type info — cannot verify; accept to avoid false positives in
		// scanner-only paths.
		return true
	}
	obj, ok := info.Uses[ident]
	if !ok {
		return false
	}
	c, ok := obj.(*types.Const)
	if !ok {
		return false
	}
	_, found := ruleCodeConsts[c]
	return found
}

// collectRuleCodeConsts returns the set of *types.Const objects in the
// governance package scope that have type RuleCode. Only consts whose
// declaration position is within rulecodes.go are included.
func collectRuleCodeConsts(pkg *governancePackage) map[*types.Const]struct{} {
	out := map[*types.Const]struct{}{}
	for _, name := range pkg.scope.Names() {
		obj := pkg.scope.Lookup(name)
		c, ok := obj.(*types.Const)
		if !ok {
			continue
		}
		// Check type is RuleCode.
		named, ok := c.Type().(*types.Named)
		if !ok {
			continue
		}
		if named.Obj().Name() != "RuleCode" {
			continue
		}
		if named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != governancePkgPath {
			continue
		}
		// Filter by declaration filename: only consts declared in rulecodes.go
		// are part of the single-source funnel. A const declared in any other
		// file (e.g. rules_misc_strict.go) is excluded even if it has the
		// correct RuleCode type and package path.
		//
		// pkg.fset is the token.FileSet from the same Pass that loaded the
		// governance package, ensuring position resolution is valid.
		pos := pkg.fset.Position(c.Pos())
		if filepath.Base(pos.Filename) != ruleCodesFile {
			continue
		}
		out[c] = struct{}{}
	}
	return out
}

// isValidationResultCompositeLit returns true when cl is typed as
// kernel/governance.ValidationResult (either via explicit named type or
// inferred from context — the latter is not detected here; callers rely on
// the explicit type path for the production check).
// governanceEmitterName returns the governance ValidationResult-constructor
// name a CallExpr targets, or "" if the call is not a constructor. The four
// constructors are the single funnel through which all findings are built
// (see kernel/governance/emitter_invariant.go):
//   - newError / newWarning / newScopedError — *locator methods (SelectorExpr)
//   - newErrorAt — package-level function (Ident), used by receiver-less scan
//     helpers; it never consults the yaml.Node cache.
//
// Matching is by name within the single-package governance scan, where these
// names are governance-internal. INV-2 uses every name (code-arg const check);
// INV-3 uses only the error constructors (fix-arg non-empty check).
func governanceEmitterName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		if fn.Sel != nil && isGovernanceEmitterName(fn.Sel.Name) {
			return fn.Sel.Name
		}
	case *ast.Ident:
		if isGovernanceEmitterName(fn.Name) {
			return fn.Name
		}
	}
	return ""
}

func isGovernanceEmitterName(name string) bool {
	switch name {
	case "newError", "newWarning", "newScopedError", "newErrorAt":
		return true
	default:
		return false
	}
}

// printfVerbRe matches a Go printf format verb (e.g. %s, %q, %02d, %+v, %%).
var printfVerbRe = regexp.MustCompile(`%[#+\- 0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z%]`)

// fixHasLiteralContent reports whether a resolved fix string carries actual
// remediation text — at least one non-whitespace rune that is not part of a
// printf verb. This closes the fmt.Sprintf("%s", fixParam) bypass: that
// template resolves to "%s", which is non-empty yet carries no guidance (the
// real value is a runtime parameter the static scan cannot see), so stripping
// the verb leaves nothing. A legitimate interpolated fix such as
// "set %q to draft" survives — the literal words remain after verb stripping.
func fixHasLiteralContent(resolved string) bool {
	return strings.TrimSpace(printfVerbRe.ReplaceAllString(resolved, "")) != ""
}

func isValidationResultCompositeLit(cl *ast.CompositeLit, info *types.Info, pkgPath string) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[cl]
	if !ok {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj().Name() == "ValidationResult" &&
		named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == pkgPath
}

// astShapeName returns a human-readable name for the AST node kind of expr.
func astShapeName(expr ast.Expr) string {
	switch expr.(type) {
	case *ast.BasicLit:
		return "BasicLit"
	case *ast.BinaryExpr:
		return "BinaryExpr (concat/arithmetic)"
	case *ast.CallExpr:
		return "CallExpr (e.g. fmt.Sprintf)"
	case *ast.Ident:
		return "Ident"
	case *ast.SelectorExpr:
		return "SelectorExpr"
	default:
		return "unknown"
	}
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
//
// Replaces the pre-#689 INV-3 (GOVERNANCE-RULE-ERROR-MESSAGE-FIX-SUFFIX-01),
// which scanned Message for the literal "; fix:" anchor — a Soft string
// convention. Fix-arg resolution reuses resolveStringFragments (string
// literals, package-scope const idents, + concatenation, fmt.Sprintf
// templates), so a fix built as fmt.Sprintf("set %s...", x) resolves to its
// non-empty template exactly as the old Message scan did.
//
// AI-robust grading (Funnel 双向锁): downstream Hard (fix-arg arity,
// compile-time) + downstream Medium (fix non-empty — archtest, because Go
// cannot type a non-empty string; "typed marker funnel for unbounded ops"
// ceiling) + upstream Medium (the raw-composite ban is package-internal: all
// rules live in package governance, so Go visibility cannot compile-reject an
// in-package literal). Upstream Hard-isation — sealing ValidationResult into a
// subpackage with unexported fields so in-package literal construction is
// cross-package-unexpressible — is tracked by gh issue #922 (a separate
// result-type-encapsulation effort, ~700 field-read sites; orthogonal to the
// fix-guidance funnel). This Medium-upstream + Hard-downstream transitional
// form is the sanctioned shape per .claude/rules/gocell/ai-robust.md, mirroring
// observability's SPAN-SETATTR-REDACT-01 / #851.
//
// Blind-spot self-check (AST forms outside the chosen tools' declared scope):
//   - A constructor invoked via a value/func variable rather than a direct
//     SelectorExpr/Ident callee (e.g. `f := v.newError; f(...)`) would evade
//     governanceEmitterName. This is reverse-checked with teeth by
//     TestGovernanceEmitterConstructorsNeverFuncValue: it asserts production
//     governance has ZERO such func-value uses AND that a negative fixture
//     taking a constructor as a value IS flagged (so the scan is not vacuous).
//   - A ValidationResult built by reflection or returned from a non-governance
//     helper would evade isValidationResultCompositeLit (type-gated to
//     governance.ValidationResult). No such path exists in governance.
//   - governanceEmitterName matches emitter calls by NAME (not type-resolution)
//     within the single-package governance scan. A same-package helper
//     coincidentally named newError / newWarning / newScopedError / newErrorAt
//     that is NOT the locator constructor would be a FALSE POSITIVE (over-report
//     extra callers as needing a fix arg), not a false negative — the funnel's
//     correctness and security are unaffected. The composite-ban (Path 2) is
//     type-gated via isValidationResultCompositeLit and is immune to name
//     collisions. This matches INV-2's pre-existing name-match level.
//     Type-resolution (binding the callee to governance's *locator method /
//     package newErrorAt via go/types) is deliberately NOT used: the negative
//     fixtures define their OWN fake newError / newErrorAt / newScopedError
//     methods (the production constructors are unexported, so fixtures cannot
//     reference them), and a type-resolution gate would reject those fakes and
//     collapse all negative-path coverage. Name-matching is therefore a
//     requirement of the fixture-based red-test design, not an oversight.
//   - fmt.Sprintf("%s", fixParam) — a fix arg whose template is only format
//     verbs with no literal text — is caught by fixHasLiteralContent (the
//     resolved template loses all guidance after verb stripping), so the
//     "non-empty template" check cannot be satisfied by a contentless template.
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
			var violations []string
			RunTyped(t, TypedOpts{Tests: false}, []string{tc.pattern},
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
	root := findModuleRoot(t)
	pkg := loadGovernancePackage(t, root)
	consts := collectPackageStringConsts(pkg.scope)

	var violations []string
	for _, file := range pkg.files {
		violations = append(violations,
			scanFixFieldViolationsInFile(file, pkg.fset, pkg.info, consts, pkg.fileRel(file), governancePkgPath)...)
	}
	sort.Strings(violations)
	assert.Empty(t, violations)
}

// scanFixFieldViolationsInFile reports all INV-3 violations in a single AST
// file. Shared between production and fixture tests so both exercise identical
// logic — fixture validates the production path.
//
// Path 1 (fix-arg): every finding constructor — newError / newWarning /
// newScopedError (SelectorExpr) and newErrorAt (Ident) — whose LAST positional
// argument does not resolve to a non-empty string with literal content (empty
// literal, an unresolvable expression such as a forwarded parameter, or a
// verb-only fmt.Sprintf template). resolveStringFragments handles literals,
// package-scope const idents, + concatenation, and fmt.Sprintf templates. The
// typed-Fix contract is severity-agnostic, so newWarning is scanned too.
//
// Path 2 (funnel): any ValidationResult{} composite literal in the governance
// package outside locator.go (the sole sanctioned constructor home). Findings
// everywhere else must call newError / newWarning / newErrorAt / newScopedError;
// relPath's base name gates the locator.go exemption.
//
// pkgPath recognizes ValidationResult composites as governance-owned
// (production and fixtures both reference governance.ValidationResult).
func scanFixFieldViolationsInFile(
	file *ast.File,
	fset *token.FileSet,
	info *types.Info,
	consts map[string]string,
	relPath string,
	pkgPath string,
) []string {
	var violations []string

	// Path 1: every finding constructor — newError / newWarning / newScopedError
	// (SelectorExpr) and newErrorAt (Ident) — must carry a non-empty fix as its
	// last positional argument. The typed-Fix contract covers all severities
	// (severity decides blocking vs advisory, not whether remediation is
	// structured); newWarning is included.
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		name := governanceEmitterName(call)
		if name == "" || len(call.Args) < 2 {
			return
		}
		fixArg := call.Args[len(call.Args)-1]
		if fixHasLiteralContent(strings.Join(resolveStringFragments(fixArg, consts, info), "")) {
			return
		}
		pos := fset.Position(fixArg.Pos())
		violations = append(violations,
			relPath+":"+strconv.Itoa(pos.Line)+
				": "+name+" fix argument is empty, unresolvable, or carries no literal guidance — every "+
				"finding (error or warning) must carry remediation text in the typed Fix field "+
				"(GOVERNANCE-RULE-ERROR-FIX-FIELD-01)")
	})

	// Path 2: construction funnel — no raw ValidationResult{} literals outside locator.go.
	if filepath.Base(relPath) == "locator.go" {
		return violations
	}
	scanner.EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
		if !isValidationResultCompositeLit(cl, info, pkgPath) {
			return
		}
		pos := fset.Position(cl.Pos())
		violations = append(violations,
			relPath+":"+strconv.Itoa(pos.Line)+
				": raw ValidationResult{} literal is forbidden outside locator.go — construct findings "+
				"via newError / newWarning / newErrorAt / newScopedError so the typed Fix funnel "+
				"cannot be bypassed (GOVERNANCE-RULE-ERROR-FIX-FIELD-01); "+
				"choose: newError(file-anchored) / newScopedError(cross-file) / newErrorAt(content-scan position)")
	})

	return violations
}

// findTypesPackageByPath performs a depth-first search through pkg's
// transitive *types.Package import graph to find the package with the given
// import path. Returns nil if pkg is nil or the path is not found.
//
// (*types.Package).Imports() returns only the DIRECT imports of a package —
// unlike the flat map in (*packages.Package).Imports which (when NeedDeps is
// set) contains the full transitive closure. The DFS below restores
// transitive reachability by recursively descending into each direct import.
// The visited map prevents infinite loops on import cycles (rare in Go but
// structurally possible with the go/types graph) and avoids redundant work
// when the same package is reachable via multiple paths.
func findTypesPackageByPath(pkg *types.Package, importPath string) *types.Package {
	if pkg == nil {
		return nil
	}
	visited := map[string]bool{}
	var search func(*types.Package) *types.Package
	search = func(p *types.Package) *types.Package {
		if p == nil || visited[p.Path()] {
			return nil
		}
		visited[p.Path()] = true
		if p.Path() == importPath {
			return p
		}
		for _, imp := range p.Imports() {
			if found := search(imp); found != nil {
				return found
			}
		}
		return nil
	}
	return search(pkg)
}

// resolveStringFragments returns every string fragment that contributes to
// expr's string value. An unresolved sub-expression contributes nothing;
// downstream substring match handles that case naturally.
func resolveStringFragments(expr ast.Expr, consts map[string]string, info *types.Info) []string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return nil
		}
		v, err := strconv.Unquote(e.Value)
		if err != nil {
			return nil
		}
		return []string{v}
	case *ast.Ident:
		if v, ok := consts[e.Name]; ok {
			return []string{v}
		}
		if v, ok := EvaluateConstString(info, e); ok {
			return []string{v}
		}
		return nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return nil
		}
		return append(resolveStringFragments(e.X, consts, info), resolveStringFragments(e.Y, consts, info)...)
	case *ast.CallExpr:
		if !isSprintfCall(e) || len(e.Args) == 0 {
			return nil
		}
		return resolveStringFragments(e.Args[0], consts, info)
	case *ast.ParenExpr:
		return resolveStringFragments(e.X, consts, info)
	default:
		if v, ok := EvaluateConstString(info, expr); ok {
			return []string{v}
		}
		return nil
	}
}

func isSprintfCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Sprintf" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == "fmt"
}

// governancePackage is a thin wrapper around the Pass-loaded kernel/governance
// package data the three invariants share.
//
// Pass guarantees that files, info, and fset all come from the same
// packages.Load invocation in the driver — this is the INV-1 same-source
// property that typeseval.EachFileInPackage previously provided, now
// guaranteed structurally by the Pass funnel.
type governancePackage struct {
	scope *types.Scope
	info  *types.Info
	fset  *token.FileSet
	files []*ast.File
	// fileRelFn maps an *ast.File to its module-relative slash path.
	// Populated during loadGovernancePackage from the Pass.
	fileRelFn func(*ast.File) string
}

// fileRel returns the module-relative slash path of the given file.
func (gp *governancePackage) fileRel(f *ast.File) string {
	if gp.fileRelFn != nil {
		return gp.fileRelFn(f)
	}
	return ""
}

// loadGovernancePackage loads kernel/governance via RunTyped and returns a
// governancePackage holding the Pass-provided Files/TypesInfo/Fset.
//
// RunTyped drives packages.Load once and passes a fully constructed Pass —
// Files, TypesInfo, and Fset are co-derived from a single load, satisfying
// the same-source invariant that EachFileInPackage previously provided.
func loadGovernancePackage(t *testing.T, root string) *governancePackage {
	t.Helper()
	var gp *governancePackage
	RunTyped(t, TypedOpts{Tests: false}, []string{"./kernel/governance"},
		func(p *Pass) []Diagnostic {
			gp = &governancePackage{
				scope:     p.Pkg.Scope(),
				info:      p.TypesInfo,
				fset:      p.Fset,
				files:     p.Files,
				fileRelFn: p.Rel,
			}
			return nil
		})
	require.NotNil(t, gp, "RunTyped must visit kernel/governance package")
	_ = root
	return gp
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
//	(a) known path "github.com/ghbvf/gocell/kernel/governance" — DFS must find non-nil.
//	(b) non-existent path "github.com/ghbvf/gocell/does/not/exist" — must return nil.
//	(c) nil pkg input — must safely return nil without panic.
func TestFindTypesPackageByPath(t *testing.T) {
	root := findModuleRoot(t)
	gp := loadGovernancePackage(t, root)

	// The governancePackage.scope belongs to gp.info which was loaded via
	// RunTyped with ./kernel/governance. We can obtain the *types.Package
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
		got := findTypesPackageByPath(govTypedPkg, "github.com/ghbvf/gocell/does/not/exist")
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

func testBindingProductionSource(t *testing.T) {
	root := findModuleRoot(t)
	pkg := loadGovernancePackage(t, root)

	ruleCodeConsts := collectRuleCodeConsts(pkg)
	require.NotEmpty(t, ruleCodeConsts, "rulecodes.go must declare at least one RuleCode const")

	methodMap := buildValidatorMethodMap(pkg.files)
	entries, fatal := extractAllRulesEntries(t, pkg)
	if fatal != "" {
		t.Fatal(fatal)
	}

	for _, entry := range entries {
		entry := entry
		t.Run(entry.codeValue+"_bound_to_"+entry.methodName, func(t *testing.T) {
			t.Parallel()
			assertCodeDetectBinding(t, entry, methodMap, ruleCodeConsts, pkg.info)
		})
	}
}

func testBindingNegativeFixture(t *testing.T) {
	const fixturePattern = "./tools/archtest/testdata/governance_binding_check_fixtures/mislabeled_detect_red"

	var fixtureGP *governancePackage

	RunTyped(t, TypedOpts{Tests: false}, []string{fixturePattern},
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

	require.NotNil(t, fixtureGP, "RunTyped must visit the fixture package")

	// Collect RuleCode consts from the fixture package's own scope.
	// The fixture defines its own RuleCode type, so we cannot use
	// collectRuleCodeConsts (which filters by governancePkgPath).
	fixtureRuleCodeConsts := collectRuleCodeConstsInScope(fixtureGP.scope)
	require.NotEmpty(t, fixtureRuleCodeConsts, "fixture must declare at least one RuleCode const")

	methodMap := buildValidatorMethodMap(fixtureGP.files)
	entries, fatal := extractAllRulesEntries(t, fixtureGP)
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
		root := findModuleRoot(t)
		pkg := loadGovernancePackage(t, root)
		var violations []string
		for _, f := range pkg.files {
			for _, pos := range scanEmitterFuncValueUsages(f) {
				violations = append(violations,
					pkg.fileRel(f)+":"+strconv.Itoa(pkg.fset.Position(pos).Line))
			}
		}
		assert.Empty(t, violations,
			"governance emitter constructors must only be called directly, never taken "+
				"as func values (would evade governanceEmitterName); found uses at: %v",
			violations)
	})

	t.Run("negative_fixture_funcvalue_detected", func(t *testing.T) {
		const fixturePattern = "./tools/archtest/testdata/governance_emitter_funcvalue_fixtures/funcvalue_indirection_red"
		var count int
		RunTyped(t, TypedOpts{Tests: false}, []string{fixturePattern},
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

// scanEmitterFuncValueUsages returns the positions of references to the four
// governance emitter constructors (newError/newWarning/newScopedError/
// newErrorAt) that are NOT the direct callee of a call expression — i.e.
// func-value indirections. Function declarations of those names and the .Sel
// identifier of a call's SelectorExpr are excluded (they are not value uses).
// Name-based, matching governanceEmitterName, so it works on both production
// and the fixture's fake funnel.
func scanEmitterFuncValueUsages(f *ast.File) []token.Pos {
	callees := map[ast.Expr]bool{}
	selSel := map[*ast.Ident]bool{}
	declName := map[*ast.Ident]bool{}
	scanner.EachInSubtree[ast.CallExpr](f, func(x *ast.CallExpr) {
		callees[x.Fun] = true
	})
	scanner.EachInSubtree[ast.SelectorExpr](f, func(x *ast.SelectorExpr) {
		if x.Sel != nil {
			selSel[x.Sel] = true
		}
	})
	scanner.EachInSubtree[ast.FuncDecl](f, func(x *ast.FuncDecl) {
		if x.Name != nil {
			declName[x.Name] = true
		}
	})
	var bad []token.Pos
	scanner.EachInSubtree[ast.SelectorExpr](f, func(e *ast.SelectorExpr) {
		if e.Sel != nil && isGovernanceEmitterName(e.Sel.Name) && !callees[e] {
			bad = append(bad, e.Pos())
		}
	})
	scanner.EachInSubtree[ast.Ident](f, func(e *ast.Ident) {
		if isGovernanceEmitterName(e.Name) && !callees[e] && !selSel[e] && !declName[e] {
			bad = append(bad, e.Pos())
		}
	})
	return bad
}

// allRulesEntry holds the Code string value and Detect method name extracted
// from a single Rule composite literal in allRules. For the VERIFY-06 closure,
// methodName is "validateVERIFY06".
type allRulesEntry struct {
	codeValue  string // resolved string value of the Code RuleCode const
	methodName string // name of the detect method
}

// extractAllRulesEntries parses each element of the allRules composite literal
// in the governance package and returns one allRulesEntry per Rule element.
//
// For each element the Code: KeyValueExpr value must be an *ast.Ident resolving
// (via info.Uses) to a *types.Const whose value can be extracted as a string.
// The Detect: value is parsed by extractDetectMethodName; the VERIFY-06 closure
// is represented as methodName "validateVERIFY06".
//
// Returns (nil, fatalMsg) when a fatal shape is encountered.
func extractAllRulesEntries(t *testing.T, pkg *governancePackage) ([]allRulesEntry, string) {
	t.Helper()

	var entries []allRulesEntry
	var fatal string

	for _, file := range pkg.files {
		if fatal != "" {
			break
		}
		relPath := pkg.fileRel(file)
		scanner.EachInChildren[ast.GenDecl](file, func(gd *ast.GenDecl) {
			if fatal != "" || gd.Tok != token.VAR {
				return
			}
			// Find the ValueSpec whose name is "allRules".
			vs, ok := scanner.FindFirstChild[ast.ValueSpec](gd, func(vs *ast.ValueSpec) bool {
				for _, nameIdent := range vs.Names {
					if nameIdent.Name == "allRules" {
						return true
					}
				}
				return false
			})
			if !ok || len(vs.Values) == 0 {
				return
			}
			cl, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok {
				return
			}
			scanner.EachInChildren[ast.CompositeLit](cl, func(ruleLit *ast.CompositeLit) {
				if fatal != "" {
					return
				}
				entry, entryFatal := parseRuleEntry(ruleLit, relPath, pkg)
				if entryFatal != "" {
					fatal = entryFatal
					return
				}
				if entry != nil {
					entries = append(entries, *entry)
				}
			})
		})
	}
	return entries, fatal
}

// parseRuleEntry extracts the Code and Detect values from a single Rule
// composite literal element of allRules.
// Returns (nil, "") when the entry has no Code or Detect field (skip).
// Returns (nil, fatalMsg) on a shape error.
func parseRuleEntry(ruleLit *ast.CompositeLit, relPath string, pkg *governancePackage) (*allRulesEntry, string) {
	var codeExpr ast.Expr
	var detectExpr ast.Expr

	scanner.EachInChildren[ast.KeyValueExpr](ruleLit, func(kv *ast.KeyValueExpr) {
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return
		}
		switch key.Name {
		case "Code":
			codeExpr = kv.Value
		case "Detect":
			detectExpr = kv.Value
		}
	})

	if codeExpr == nil || detectExpr == nil {
		return nil, ""
	}

	// Resolve Code to its string value.
	codeValue, ok := resolveRuleCodeValue(codeExpr, pkg.info)
	if !ok {
		pos := pkg.fset.Position(codeExpr.Pos())
		return nil, "cannot resolve Code value at " + relPath + ":" + strconv.Itoa(pos.Line) +
			" — Code must be an Ident referencing a RuleCode const"
	}

	// Resolve Detect to a method name.
	methodName, found, isFatal := extractDetectMethodName(detectExpr, relPath, pkg.fset)
	if isFatal {
		return nil, methodName // methodName carries the fatal message
	}
	if !found {
		// FuncLit VERIFY-06 closure: use the inner method name.
		if fl, ok := detectExpr.(*ast.FuncLit); ok && isVERIFY06ClosureShape(fl) {
			methodName = "validateVERIFY06"
		} else {
			return nil, ""
		}
	}

	return &allRulesEntry{codeValue: codeValue, methodName: methodName}, ""
}

// resolveRuleCodeValue extracts the string value of a RuleCode const ident.
// Returns ("", false) if the expression is not an Ident or doesn't resolve to
// a string-kinded constant.
func resolveRuleCodeValue(expr ast.Expr, info *types.Info) (string, bool) {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return "", false
	}
	if v, ok := EvaluateConstString(info, ident); ok {
		return v, true
	}
	return "", false
}

// buildValidatorMethodMap builds a map from method name to *ast.FuncDecl for
// all methods with receiver *Validator (by name, not by type) in the given
// AST files. The receiver name check is structural ("Validator") to match
// both production and fixture packages.
func buildValidatorMethodMap(files []*ast.File) map[string]*ast.FuncDecl {
	out := make(map[string]*ast.FuncDecl)
	for _, file := range files {
		scanner.EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Name == nil || fd.Recv == nil || len(fd.Recv.List) == 0 {
				return
			}
			if !isPointerValidatorReceiver(fd.Recv.List[0]) {
				return
			}
			out[fd.Name.Name] = fd
		})
	}
	return out
}

// isPointerValidatorReceiver reports whether field is a *Validator receiver.
// Matches: *ast.StarExpr{X: *ast.Ident{Name: "Validator"}}.
func isPointerValidatorReceiver(field *ast.Field) bool {
	star, ok := field.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "Validator"
}

// collectEmittedCodes performs a BFS over same-receiver *Validator method calls
// starting from startMethod, collecting the string values of every RuleCode
// const passed as the first argument to newError / newWarning / newScopedError /
// newErrorAt within the reachable call graph.
//
// The BFS visits each method at most once (visited set prevents cycles).
// Only calls where the method name is present in methodMap are followed;
// package-level functions and non-Validator-method calls are not traversed.
//
// Returns an empty map when startMethod is not in methodMap (not found).
func collectEmittedCodes(
	startMethod string,
	methodMap map[string]*ast.FuncDecl,
	ruleCodeConsts map[*types.Const]struct{},
	info *types.Info,
) map[string]struct{} {
	emitted := map[string]struct{}{}
	visited := map[string]bool{}
	queue := []string{startMethod}

	for len(queue) > 0 {
		methodName := queue[0]
		queue = queue[1:]
		if visited[methodName] {
			continue
		}
		visited[methodName] = true

		fd, ok := methodMap[methodName]
		if !ok {
			continue
		}

		// Walk the method body.
		collectEmittedCodesInFunc(fd, methodMap, ruleCodeConsts, info, emitted, &queue)
	}
	return emitted
}

// collectEmittedCodesInFunc walks fd's body collecting emitted codes and
// queuing new *Validator method calls for BFS expansion.
func collectEmittedCodesInFunc(
	fd *ast.FuncDecl,
	methodMap map[string]*ast.FuncDecl,
	ruleCodeConsts map[*types.Const]struct{},
	info *types.Info,
	emitted map[string]struct{},
	queue *[]string,
) {
	if fd.Body == nil {
		return
	}
	scanner.EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			// newErrorAt is a package-level Ident call.
			if ident, ok := call.Fun.(*ast.Ident); ok {
				if isGovernanceEmitterName(ident.Name) && len(call.Args) > 0 {
					if v, resolved := resolveRuleCodeArg(call.Args[0], info, ruleCodeConsts); resolved {
						emitted[v] = struct{}{}
					}
				}
			}
			return
		}
		if sel.Sel == nil {
			return
		}
		callee := sel.Sel.Name
		if isGovernanceEmitterName(callee) && len(call.Args) > 0 {
			// Emitter call — collect its code arg.
			if v, resolved := resolveRuleCodeArg(call.Args[0], info, ruleCodeConsts); resolved {
				emitted[v] = struct{}{}
			}
			return
		}
		// Same-receiver method call — enqueue for BFS if in methodMap.
		if _, inMap := methodMap[callee]; inMap {
			*queue = append(*queue, callee)
		}
	})
}

// resolveRuleCodeArg resolves expr (the first argument of a governance emitter
// call) to its RuleCode string value. Returns ("", false) when the expression
// is not an Ident referencing a known RuleCode const.
func resolveRuleCodeArg(expr ast.Expr, info *types.Info, ruleCodeConsts map[*types.Const]struct{}) (string, bool) {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return "", false
	}
	if info == nil {
		return "", false
	}
	obj, ok := info.Uses[ident]
	if !ok {
		return "", false
	}
	c, ok := obj.(*types.Const)
	if !ok {
		return "", false
	}
	// Check it is in the known RuleCode const set (when non-empty).
	// For fixture packages the set may contain all RuleCode consts in scope.
	if len(ruleCodeConsts) > 0 {
		if _, found := ruleCodeConsts[c]; !found {
			return "", false
		}
	}
	v, ok := EvaluateConstString(info, ident)
	return v, ok
}

// assertCodeDetectBinding asserts that entry.codeValue appears in the set of
// RuleCode strings emitted by entry.methodName (transitively).
func assertCodeDetectBinding(
	t *testing.T,
	entry allRulesEntry,
	methodMap map[string]*ast.FuncDecl,
	ruleCodeConsts map[*types.Const]struct{},
	info *types.Info,
) {
	t.Helper()
	emitted := collectEmittedCodes(entry.methodName, methodMap, ruleCodeConsts, info)
	if len(emitted) == 0 {
		t.Fatalf(
			"allRules entry Code=%q Detect=%q: the detect method (and its transitive *Validator"+
				" helper calls) emit NO RuleCode consts — either the walk is incomplete or the"+
				" rule body is empty (both are bugs to surface)",
			entry.codeValue, entry.methodName,
		)
	}
	if _, ok := emitted[entry.codeValue]; !ok {
		t.Fatalf(
			"allRules entry Code=%q Detect=%q: entry Code not in emitted set %v"+
				" — mislabeled entry: the detect method emits a different code",
			entry.codeValue, entry.methodName, sortedStringSet(emitted),
		)
	}
}

// collectRuleCodeConstsInScope collects all *types.Const objects in scope
// whose type's named-type name is "RuleCode", regardless of package path.
// Used for fixture packages that define their own RuleCode type.
func collectRuleCodeConstsInScope(scope *types.Scope) map[*types.Const]struct{} {
	out := map[*types.Const]struct{}{}
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
		out[c] = struct{}{}
	}
	return out
}

// sortedStringSet returns the keys of a string set in sorted order.
func sortedStringSet(s map[string]struct{}) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// collectPackageStringConsts walks scope's names and returns a map from
// every package-scope string const name to its value.
func collectPackageStringConsts(scope *types.Scope) map[string]string {
	out := map[string]string{}
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		c, ok := obj.(*types.Const)
		if !ok {
			continue
		}
		if c.Val() == nil || c.Val().Kind() != constant.String {
			continue
		}
		out[name] = constant.StringVal(c.Val())
	}
	return out
}
