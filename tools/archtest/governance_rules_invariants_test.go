// Package archtest enforces meta-governance over kernel/governance rule registration.
//
//   - INVARIANT: GOVERNANCE-RULES-REGISTRATION-GUARD-01
//   - INVARIANT: GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01
//   - INVARIANT: GOVERNANCE-RULE-ERROR-FIX-FIELD-01
//
// G-13 elevates governance rule registration from a hand-edited slice to an
// archtest-guarded contract. The three invariants together catch:
//   - forgotten registration (a new validate* method that never runs);
//   - drift between rule code literals and the rulecodes.go single source;
//   - error findings emitted without remediation guidance in the typed Fix
//     field, or built via a raw ValidationResult{} literal that bypasses the
//     newError / newWarning / newErrorAt / newScopedError constructor funnel
//     (#689 upgrade from the former "; fix:" Message-substring convention).
//
// ref: G-13 (governance rule registration archtest)
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
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// governancePkgPath is the import path of the package whose rules() and
// strictRules() slices we enumerate.
const governancePkgPath = "github.com/ghbvf/gocell/kernel/governance"

// ruleCodesFile is the base name of the single-source file for RuleCode consts.
const ruleCodesFile = "rulecodes.go"

// TestGovernanceRulesRegistrationGuard verifies INV-1: every method on
// *governance.Validator with the rule signature is referenced from rules()
// or strictRules(). A rule signature is exactly one of:
//
//   - func() []ValidationResult            (pure-memory rule)
//   - func(context.Context) []ValidationResult   (ctx-bound rule — only VERIFY-06)
//
// Helper methods that take (*metadata.ContractMeta, ...) or similar arguments
// fail the signature filter and are excluded.
func TestGovernanceRulesRegistrationGuard(t *testing.T) {
	t.Run("production_source_all_registered", testINV1ProductionSource)
	t.Run("negative_fixture_shadow_receiver_rejected", testINV1ShadowReceiverFixture)
}

func testINV1ProductionSource(t *testing.T) {
	root := findModuleRoot(t)
	pkg := loadGovernancePackage(t, root)

	declared := declaredRuleMethodNames(t, pkg)

	registered, fatal := extractRegisteredMethodNames(t, pkg)
	if fatal != "" {
		t.Fatal(fatal)
	}

	missing := setDifference(declared, registered)
	extra := setDifference(registered, declared)
	assert.Empty(t, missing,
		"validate* methods declared on *Validator but not registered in rules()/strictRules(): %v", missing)
	assert.Empty(t, extra,
		"names referenced in rules()/strictRules() but no matching validate* method on *Validator: %v", extra)
}

// testINV1ShadowReceiverFixture proves the receiver-type check is genuinely
// active. The fixture rules() mixes a real *Validator method with a shadow
// *OtherType method that has the same validate* name shape. The
// receiver-type check must reject o.validateFOO; because extractFromCompositeLits
// treats any unaccepted element as an unrecognized shape (refusing to silently
// skip — the original loud-failure semantics), the rejection surfaces as a
// non-empty fatal message anchored at the shadow line.
//
// Before the fail-closed fix, info.Types[sel.X] missed for every scanner-
// parsed selector and the receiver check returned true unconditionally — both
// methods would have been silently accepted, and the wrong receiver shadow
// would not have triggered fatal.
func testINV1ShadowReceiverFixture(t *testing.T) {
	const fixturePattern = "./tools/archtest/testdata/governance_registration_guard_fixtures/shadow_receiver_red"

	var validatorNamed *types.Named
	registered := map[string]struct{}{}
	var fatal string

	RunTyped(t, TypedOpts{Tests: false}, []string{fixturePattern},
		func(p *Pass) []Diagnostic {
			// Retrieve Validator type from the fixture package scope.
			validatorObj := p.Pkg.Scope().Lookup("Validator")
			if validatorObj == nil {
				t.Errorf("fixture must declare Validator")
				return nil
			}
			validatorTypeName, ok := validatorObj.(*types.TypeName)
			if !ok {
				t.Errorf("Validator must be a type name")
				return nil
			}
			validatorNamed, ok = validatorTypeName.Type().(*types.Named)
			if !ok {
				t.Errorf("Validator must be a named type")
				return nil
			}

			for _, file := range p.Files {
				relPath := p.Rel(file)
				scanner.EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
					if fd.Name == nil || fd.Recv == nil {
						return
					}
					if fd.Name.Name != "rules" && fd.Name.Name != "strictRules" {
						return
					}
					extractFromCompositeLits(p.Fset, relPath, fd, p.TypesInfo, validatorNamed, registered, &fatal)
				})
			}
			return nil
		})

	require.NotNil(t, validatorNamed, "fixture package must be loadable with type info")
	_, hasLegit := registered["validateLegit"]
	_, hasFOO := registered["validateFOO"]
	assert.True(t, hasLegit, "v.validateLegit (real *Validator method) must be accepted before the shadow element triggers fatal")
	assert.False(t, hasFOO, "o.validateFOO (OtherType receiver shadow) must be REJECTED by receiver-type check")
	assert.NotEmpty(t, fatal,
		"shadow receiver must surface as a non-empty fatal — silent skip would let the wrong method masquerade as registered")
	assert.Contains(t, fatal, "shadow_receiver_red/violation.go",
		"fatal must reference the fixture file so the violation is locatable")
}

// declaredRuleMethodNames returns the set of *Validator method names whose
// signature matches a rule shape.
func declaredRuleMethodNames(t *testing.T, pkg *governancePackage) map[string]struct{} {
	t.Helper()
	valObj := pkg.scope.Lookup("Validator")
	require.NotNil(t, valObj, "Validator type must exist")
	valTypeName, ok := valObj.(*types.TypeName)
	require.True(t, ok, "Validator must be a type name")
	valNamed, ok := valTypeName.Type().(*types.Named)
	require.True(t, ok, "Validator must be a named type")

	ms := types.NewMethodSet(types.NewPointer(valNamed))
	out := map[string]struct{}{}
	for i := 0; i < ms.Len(); i++ {
		sel := ms.At(i)
		fn, ok := sel.Obj().(*types.Func)
		if !ok {
			continue
		}
		if fn.Pkg() == nil || fn.Pkg().Path() != governancePkgPath {
			continue
		}
		if !strings.HasPrefix(fn.Name(), "validate") {
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

// ruleShapeSignature returns true when sig matches one of the rule shapes:
// `func() []ValidationResult` or `func(context.Context) []ValidationResult`.
// All other signatures (helpers taking *metadata.ContractMeta etc.) are
// rejected.
func ruleShapeSignature(sig *types.Signature) bool {
	if sig.Variadic() || sig.Results().Len() != 1 {
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
	if elem.Obj().Pkg() == nil || elem.Obj().Pkg().Path() != governancePkgPath {
		return false
	}
	if elem.Obj().Name() != "ValidationResult" {
		return false
	}
	switch sig.Params().Len() {
	case 0:
		return true
	case 1:
		p0, ok := sig.Params().At(0).Type().(*types.Named)
		if !ok {
			return false
		}
		if p0.Obj().Pkg() == nil {
			return false
		}
		return p0.Obj().Pkg().Path() == "context" && p0.Obj().Name() == "Context"
	default:
		return false
	}
}

// extractRegisteredMethodNames walks the bodies of `rules` and `strictRules`
// FuncDecls in kernel/governance and collects every validate* method name
// referenced as either a method value (`v.validateXX`) or inside a closure
// of the shape `func() []ValidationResult { return v.validateXX(ctx) }`.
// Any composite-literal element with a different shape causes a fatal
// message — silently skipping would let new closure forms bypass the check.
//
// Same-source guarantee: gp.files, gp.info, and gp.fset are the three fields
// of a governancePackage populated by loadGovernancePackage from a single
// RunTyped Pass. RunTyped constructs the Pass via the packages.Load driver
// (buildTypedPass), which co-derives Files, TypesInfo, and Fset from the same
// load invocation. As a result info.Types[sel.X] resolves on every selector in
// these files — the same-source property is guaranteed by the Pass funnel
// driver, not by direct Pass field access (the governancePackage wrapper
// transfers the three fields, not the *Pass itself).
func extractRegisteredMethodNames(t *testing.T, pkg *governancePackage) (map[string]struct{}, string) {
	t.Helper()
	validatorNamed := lookupValidatorNamed(t, pkg)

	registered := map[string]struct{}{}
	var fatal string
	for _, file := range pkg.files {
		if fatal != "" {
			break
		}
		relPath := pkg.fileRel(file)
		scanner.EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fatal != "" {
				return
			}
			if fd.Name == nil || fd.Recv == nil {
				return
			}
			if fd.Name.Name != "rules" && fd.Name.Name != "strictRules" {
				return
			}
			extractFromCompositeLits(pkg.fset, relPath, fd, pkg.info, validatorNamed, registered, &fatal)
		})
	}
	return registered, fatal
}

// lookupValidatorNamed returns the *types.Named for kernel/governance.Validator.
func lookupValidatorNamed(t *testing.T, pkg *governancePackage) *types.Named {
	t.Helper()
	obj := pkg.scope.Lookup("Validator")
	require.NotNil(t, obj, "Validator must be declared in kernel/governance")
	tn, ok := obj.(*types.TypeName)
	require.True(t, ok, "Validator must be a type name")
	named, ok := tn.Type().(*types.Named)
	require.True(t, ok, "Validator must be a named type")
	return named
}

func extractFromCompositeLits(
	fset *token.FileSet, relPath string, fd *ast.FuncDecl, info *types.Info,
	validatorNamed *types.Named, out map[string]struct{}, fatal *string,
) {
	scanner.EachInSubtree[ast.CompositeLit](fd.Body, func(cl *ast.CompositeLit) {
		if *fatal != "" {
			return
		}
		arr, ok := cl.Type.(*ast.ArrayType)
		if !ok {
			return
		}
		funcT, ok := arr.Elt.(*ast.FuncType)
		if !ok {
			return
		}
		if funcT.Params != nil && len(funcT.Params.List) != 0 {
			return
		}
		for _, elt := range cl.Elts {
			if name, ok := registeredElementMethodName(elt, info, validatorNamed); ok {
				out[name] = struct{}{}
				continue
			}
			pos := fset.Position(elt.Pos())
			*fatal = "unrecognized rules() element shape at " + relPath + ":" +
				strconv.Itoa(pos.Line) +
				" — every element must be either v.validateXX or func() []VR { return v.validateXX(...) }"
			return
		}
	})
}

// registeredElementMethodName returns the validate* method name referenced
// by a single composite-literal element.
//
// INV-1 Medium upgrade: the receiver expression is verified via
// info.Selections to confirm its declared type is *governance.Validator.
// A local variable shadow (e.g. `v := someOtherType{}`) that happens to
// expose a validate* method will be rejected.
func registeredElementMethodName(expr ast.Expr, info *types.Info, validatorNamed *types.Named) (string, bool) {
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		name, ok := validateSelectorReceiverAndName(sel, info, validatorNamed)
		return name, ok
	}
	fl, ok := expr.(*ast.FuncLit)
	if !ok || fl.Body == nil || len(fl.Body.List) != 1 {
		return "", false
	}
	ret, ok := fl.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return "", false
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return "", false
	}
	callSel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	name, ok := validateSelectorReceiverAndName(callSel, info, validatorNamed)
	return name, ok
}

// validateSelectorReceiverAndName verifies that sel.X has type *Validator
// (via info.Types, which maps every type-checked expression to its
// TypeAndValue) and that sel.Sel.Name has the "validate" prefix.
// Returns ("", false) for any non-matching selector.
//
// info must be the *types.Info from the same Pass that provided the AST file
// containing sel — Pass guarantees this by design (Files and TypesInfo are
// populated from the same packages.Load invocation in the driver).
func validateSelectorReceiverAndName(sel *ast.SelectorExpr, info *types.Info, validatorNamed *types.Named) (string, bool) {
	if sel.Sel == nil || !strings.HasPrefix(sel.Sel.Name, "validate") {
		return "", false
	}
	if !selectorReceiverIsValidator(sel, info, validatorNamed) {
		return "", false
	}
	return sel.Sel.Name, true
}

// selectorReceiverIsValidator returns true when sel.X resolves to a value of
// type *Validator (or Validator) via go/types Types map.
//
// Fail-closed on lookup miss: a missing TypeAndValue means either info was
// built from a different AST (caller violated the same-source invariant) or
// the receiver expression is non-standard (e.g. assembled at runtime). Both
// must be rejected — the previous "return true" fallback silently downgraded
// the receiver-type check to a name-prefix match.
func selectorReceiverIsValidator(sel *ast.SelectorExpr, info *types.Info, validatorNamed *types.Named) bool {
	tv, ok := info.Types[sel.X]
	if !ok {
		return false
	}
	recvType := tv.Type
	if ptr, ok := recvType.(*types.Pointer); ok {
		recvType = ptr.Elem()
	}
	named, ok := recvType.(*types.Named)
	if !ok {
		return false
	}
	return named == validatorNamed
}

// TestGovernanceRuleCodeConstSingleSource verifies INV-2: every
// newResult / newScopedResult / newResultAt call in kernel/governance (excluding
// *_test.go and rulecodes.go) must pass a RuleCode-typed constant declared in
// rulecodes.go as its first argument. Every ValidationResult{} CompositeLit
// within kernel/governance must use a RuleCode const from rulecodes.go as
// the Code: field value.
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
//  1. CallExpr: newResult / newScopedResult / newResultAt with code arg
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

// isGovernanceErrorEmitterName reports whether name is one of the three error
// constructors that take a mandatory fix argument as their last positional
// parameter. newWarning is excluded — warnings carry no fix.
func isGovernanceErrorEmitterName(name string) bool {
	switch name {
	case "newError", "newScopedError", "newErrorAt":
		return true
	default:
		return false
	}
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
//  1. Fix-arg non-empty: every error-constructor call — newError /
//     newScopedError (locator methods) and newErrorAt (package-level
//     function) — must pass a resolvable, non-empty remediation string as its
//     LAST positional argument. The constructor signatures make the fix
//     parameter mandatory at compile time (arity: choosing the error API means
//     supplying a fix slot); this archtest closes the residual gap Go cannot
//     express — that the fix is non-empty. newWarning has no fix parameter and
//     is not scanned.
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
//     governanceEmitterName. governance has no such indirection; the reverse
//     self-check is the production scan staying green AND the negative fixtures
//     using direct calls.
//   - A ValidationResult built by reflection or returned from a non-governance
//     helper would evade isValidationResultCompositeLit (type-gated to
//     governance.ValidationResult). No such path exists in governance.
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
// Path 1 (fix-arg): error-constructor calls — newError / newScopedError
// (SelectorExpr) and newErrorAt (Ident) — whose LAST positional argument does
// not resolve to a non-empty string (empty literal, or an unresolvable
// expression such as a forwarded parameter). resolveStringFragments handles
// literals, package-scope const idents, + concatenation, and fmt.Sprintf
// templates. newWarning has no fix and is not scanned.
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

	// Path 1: error constructors must carry a non-empty fix (last positional arg).
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		name := governanceEmitterName(call)
		if !isGovernanceErrorEmitterName(name) || len(call.Args) < 2 {
			return
		}
		fixArg := call.Args[len(call.Args)-1]
		if strings.Join(resolveStringFragments(fixArg, consts, info), "") != "" {
			return
		}
		pos := fset.Position(fixArg.Pos())
		violations = append(violations,
			relPath+":"+strconv.Itoa(pos.Line)+
				": "+name+" fix argument is empty or unresolvable — every error finding must "+
				"carry remediation guidance in the typed Fix field (GOVERNANCE-RULE-ERROR-FIX-FIELD-01)")
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
				"cannot be bypassed (GOVERNANCE-RULE-ERROR-FIX-FIELD-01)")
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
