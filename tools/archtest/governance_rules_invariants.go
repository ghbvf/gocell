package archtest

// governance_rules_invariants.go — importable Check* functions for the five
// GOVERNANCE-* invariants (#1638 M3 PR-8a).
//
// Non-test home so external Cell repos can import and run these rules; Go
// never compiles a dependency's _test.go. GoCell's own Test* functions in
// governance_rules_invariants_test.go dogfood the same Check* (single source,
// no parallel rule body).
//
// # Not registered (register=none)
//
// All five rules are gocell-internal-layout funnels — they scan kernel/governance
// engine internals (allRules, rulecodes.go, rule method signatures) that do not
// exist in an external module. Running against an external repo produces vacuous-
// green or false-red. Enforced in GoCell via the corresponding Test* functions.
//
// Rules:
//   - CheckGovernanceRulesRegistrationGuard  (GOVERNANCE-RULES-REGISTRATION-GUARD-01)
//   - CheckGovernanceRuleCodeConstSingleSource (GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01)
//   - CheckGovernanceRuleErrorFixField         (GOVERNANCE-RULE-ERROR-FIX-FIELD-01)
//   - CheckGovernanceRuleCodeDetectBinding     (GOVERNANCE-RULE-CODE-DETECT-BINDING-01)
//   - CheckGovernanceEmitterConstructorsNeverFuncValue (GOVERNANCE-RULE-EMITTER-CONSTRUCTORS-NEVER-FUNCVALUE-01)

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// governancePkgPath is the import path of the kernel/governance package.
// Derived from PlatformModulePath so a module rename updates exactly one place
// (ARCHTEST-MODULE-PATH-FUNNEL-01).
const governancePkgPath = PlatformFrameworkModulePath + "/kernel/governance"

// governanceRelDir is the module-relative directory of kernel/governance,
// derived from governancePkgPath so a module rename updates exactly one place
// (ARCHTEST-MODULE-PATH-FUNNEL-01). Used to anchor package-level guard
// diagnostics (e.g. "rulecodes.go must declare a const") to a clickable path
// instead of degrading to ":0:".
var governanceRelDir = strings.TrimPrefix(governancePkgPath, PlatformModulePath+"/")

// ruleCodesFile is the base name of the single-source file for RuleCode consts.
const ruleCodesFile = "rulecodes.go"

// CheckGovernanceRulesRegistrationGuard runs GOVERNANCE-RULES-REGISTRATION-GUARD-01
// and returns diagnostics. It does NOT call t.Errorf.
//
// Verifies set equality: declared == registered, where:
//   - declared = *Validator methods with signature func() []ValidationResult
//     AND name prefix in {validate, checkDEP, checkCH}.
//   - registered = method names extracted from the allRules package-level var
//     in rules_registry.go.
//
// Not registered (register=none): gocell-internal-layout funnel; vacuous-green/
// false-red in an external module; enforced in GoCell via
// TestGovernanceRulesRegistrationGuard.
//
// AI-robust grading (INV-1):
//   - downstream Hard: every allRules Detect must resolve to a real *Validator
//     method expression — a typo'd method expression fails to compile.
//   - upstream Medium: declared==registered set equality is archtest. The naming
//     prefix {validate,checkDEP,checkCH} is a naming convention (Soft tier); the
//     prefix filter is kept at the same tier as the pre-M3 validate* filter.
func CheckGovernanceRulesRegistrationGuard(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	pkg := loadGovernancePackage(t, root)

	declared := declaredRuleMethodNames(t, pkg)
	registered, registryRel, fatal := extractRegisteredFromAllRules(pkg)
	if registryRel == "" {
		registryRel = governanceRelDir
	}
	if fatal != "" {
		return []Diagnostic{diagFile(registryRel, "GOVERNANCE-RULES-REGISTRATION-GUARD-01 fatal: "+fatal)}
	}

	var diags []Diagnostic
	for name := range declared {
		if _, ok := registered[name]; !ok {
			diags = append(diags, diagFile(registryRel,
				"rule-shaped method declared on *Validator but not registered in allRules: "+name))
		}
	}
	for name := range registered {
		if _, ok := declared[name]; !ok {
			diags = append(diags, diagFile(registryRel,
				"name referenced in allRules Detect but no matching rule-shaped method on *Validator: "+name))
		}
	}
	return diags
}

// CheckGovernanceRuleCodeConstSingleSource runs GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01
// and returns diagnostics. It does NOT call t.Errorf.
//
// Every newError/newWarning/newScopedError/newErrorAt call in kernel/governance
// (excluding *_test.go and rulecodes.go) must pass a RuleCode-typed constant
// declared in rulecodes.go as its first argument. Every ValidationResult{}
// composite literal must use a RuleCode const from rulecodes.go as Code: value.
//
// Not registered (register=none): gocell-internal-layout funnel; vacuous-green/
// false-red in an external module; enforced in GoCell via
// TestGovernanceRuleCodeConstSingleSource.
func CheckGovernanceRuleCodeConstSingleSource(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	pkg := loadGovernancePackage(t, root)

	ruleCodeConsts := collectRuleCodeConsts(pkg)
	if len(ruleCodeConsts) == 0 {
		return []Diagnostic{diagFile(governanceRelDir+"/"+ruleCodesFile,
			"rulecodes.go must declare at least one RuleCode const")}
	}

	var diags []Diagnostic
	for _, file := range pkg.files {
		base := filepath.Base(pkg.fileRel(file))
		if base == ruleCodesFile || base == "locator.go" {
			continue
		}
		diags = append(diags,
			scanINV2ViolationsInFile(file, pkg.fset, pkg.info, ruleCodeConsts, pkg.fileRel(file), governancePkgPath)...)
	}
	return diags
}

// CheckGovernanceRuleErrorFixField runs GOVERNANCE-RULE-ERROR-FIX-FIELD-01
// and returns diagnostics. It does NOT call t.Errorf.
//
// Two properties:
//  1. Fix-arg non-empty: every finding-constructor call must pass a resolvable,
//     non-empty remediation string as its LAST positional argument.
//  2. Construction funnel: no raw ValidationResult{} composite literal may
//     appear in the governance package outside locator.go.
//
// Not registered (register=none): gocell-internal-layout funnel; vacuous-green/
// false-red in an external module; enforced in GoCell via
// TestGovernanceRuleErrorFixField.
func CheckGovernanceRuleErrorFixField(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	pkg := loadGovernancePackage(t, root)
	consts := collectPackageStringConsts(pkg.scope)

	var diags []Diagnostic
	for _, file := range pkg.files {
		diags = append(diags,
			scanFixFieldViolationsInFile(file, pkg.fset, pkg.info, consts, pkg.fileRel(file), governancePkgPath)...)
	}
	return diags
}

// CheckGovernanceRuleCodeDetectBinding runs GOVERNANCE-RULE-CODE-DETECT-BINDING-01
// and returns diagnostics. It does NOT call t.Errorf.
//
// For every entry in kernel/governance allRules, the Rule.Code value must appear
// as the first argument of at least one newError/newWarning/newScopedError/
// newErrorAt call reachable from the entry's Detect method (BFS over same-receiver
// *Validator method calls).
//
// Not registered (register=none): gocell-internal-layout funnel; vacuous-green/
// false-red in an external module; enforced in GoCell via
// TestGovernanceRuleCodeDetectBinding.
//
// AI-robust grading: Medium (type-aware archtest AST walk). Hard path does not
// exist — Rule.Code is structurally separate from the detect body's newError
// argument; no type-system mechanism can enforce their equality at compile time.
func CheckGovernanceRuleCodeDetectBinding(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	pkg := loadGovernancePackage(t, root)

	ruleCodeConsts := collectRuleCodeConsts(pkg)
	if len(ruleCodeConsts) == 0 {
		return []Diagnostic{diagFile(governanceRelDir+"/"+ruleCodesFile,
			"rulecodes.go must declare at least one RuleCode const")}
	}

	methodMap := buildValidatorMethodMap(pkg.files)
	entries, registryRel, fatal := extractAllRulesEntries(pkg)
	if registryRel == "" {
		registryRel = governanceRelDir
	}
	if fatal != "" {
		return []Diagnostic{diagFile(registryRel, "GOVERNANCE-RULE-CODE-DETECT-BINDING-01 fatal: "+fatal)}
	}

	var diags []Diagnostic
	for _, entry := range entries {
		diags = append(diags, checkCodeDetectBinding(entry, methodMap, ruleCodeConsts, pkg.info)...)
	}
	return diags
}

// checkCodeDetectBinding returns diagnostics when entry.codeValue is not emitted
// by entry.methodName (transitively). Returns nil when the binding is correct.
func checkCodeDetectBinding(
	entry allRulesEntry,
	methodMap map[string]*ast.FuncDecl,
	ruleCodeConsts map[*types.Const]struct{},
	info *types.Info,
) []Diagnostic {
	emitted := collectEmittedCodes(entry.methodName, methodMap, ruleCodeConsts, info)
	if len(emitted) == 0 {
		return []Diagnostic{diagAt(entry.rel, entry.line,
			"allRules entry Code="+entry.codeValue+" Detect="+entry.methodName+
				": detect method emits NO RuleCode consts — walk incomplete or rule body empty")}
	}
	if _, ok := emitted[entry.codeValue]; !ok {
		return []Diagnostic{diagAt(entry.rel, entry.line,
			"allRules entry Code="+entry.codeValue+" Detect="+entry.methodName+
				": entry Code not in emitted set "+strings.Join(sortedStringSet(emitted), ",")+
				" — mislabeled entry: detect method emits a different code")}
	}
	return nil
}

// CheckGovernanceEmitterConstructorsNeverFuncValue runs
// GOVERNANCE-RULE-EMITTER-CONSTRUCTORS-NEVER-FUNCVALUE-01 and returns
// diagnostics. It does NOT call t.Errorf.
//
// Verifies that kernel/governance contains ZERO references to the emitter
// constructors (newError/newWarning/newScopedError/newErrorAt) that are not
// the direct callee of a call expression (i.e. no func-value indirections).
//
// Not registered (register=none): gocell-internal-layout funnel; vacuous-green/
// false-red in an external module; enforced in GoCell via
// TestGovernanceEmitterConstructorsNeverFuncValue.
func CheckGovernanceEmitterConstructorsNeverFuncValue(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	pkg := loadGovernancePackage(t, root)

	var diags []Diagnostic
	for _, f := range pkg.files {
		for _, pos := range scanEmitterFuncValueUsages(f) {
			diags = append(diags, Diagnostic{
				Rel:     pkg.fileRel(f),
				Line:    pkg.fset.Position(pos).Line,
				Message: "governance emitter constructor taken as func value (evades governanceEmitterName)",
			})
		}
	}
	return diags
}

// ----- governancePackage -----

// governancePackage is a thin wrapper around the Pass-loaded kernel/governance
// package data the five invariants share.
//
// Pass guarantees that files, info, and fset all come from the same
// packages.Load invocation in the driver.
type governancePackage struct {
	scope *types.Scope
	info  *types.Info
	fset  *token.FileSet
	files []*ast.File
	// fileRelFn maps an *ast.File to its module-relative slash path.
	fileRelFn func(*ast.File) string
}

// fileRel returns the module-relative slash path of the given file.
func (gp *governancePackage) fileRel(f *ast.File) string {
	if gp.fileRelFn != nil {
		return gp.fileRelFn(f)
	}
	return ""
}

// loadGovernancePackage loads kernel/governance via Run(t, Typed(...)) and returns
// a governancePackage. Files, TypesInfo, and Fset are co-derived from a single
// load, satisfying the same-source invariant.
func loadGovernancePackage(t *testing.T, root string) *governancePackage {
	t.Helper()
	var gp *governancePackage
	Run(t, Typed(TypedOpts{Tests: false}, []string{"./framework/kernel/governance"}),
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
	_ = root
	return gp
}

// ----- INV-1 helpers -----

// declaredRuleMethodNames returns the set of *Validator method names whose
// signature matches the rule shape AND whose name has a rule prefix.
//
// Rule shape: func() []ValidationResult (zero params, single []ValidationResult
// result). The ctx-bound form is intentionally excluded.
// Rule prefix set: {validate, checkDEP, checkCH}.
func declaredRuleMethodNames(t *testing.T, pkg *governancePackage) map[string]struct{} {
	t.Helper()
	valNamed, pkgPath := resolveValidatorNamedType(t, pkg)

	ms := types.NewMethodSet(types.NewPointer(valNamed))
	out := map[string]struct{}{}
	for i := 0; i < ms.Len(); i++ {
		if name, ok := validatorRuleMethodAt(ms, i, pkgPath); ok {
			out[name] = struct{}{}
		}
	}
	return out
}

// resolveValidatorNamedType looks up the Validator named type in pkg and returns
// (namedType, packagePath). Calls t.Fatal if Validator is missing or malformed.
func resolveValidatorNamedType(t *testing.T, pkg *governancePackage) (*types.Named, string) {
	t.Helper()
	valObj := pkg.scope.Lookup("Validator")
	if valObj == nil {
		t.Fatal("Validator type must exist in package")
	}
	valTypeName, ok := valObj.(*types.TypeName)
	if !ok {
		t.Fatal("Validator must be a type name")
	}
	valNamed, ok := valTypeName.Type().(*types.Named)
	if !ok {
		t.Fatal("Validator must be a named type")
	}
	var pkgPath string
	if valTypeName.Pkg() != nil {
		pkgPath = valTypeName.Pkg().Path()
	}
	return valNamed, pkgPath
}

// validatorRuleMethodAt checks the i-th method in ms against the pkgPath filter
// and rule-shape/prefix predicates. Returns (name, true) when all checks pass.
func validatorRuleMethodAt(ms *types.MethodSet, i int, pkgPath string) (string, bool) {
	sel := ms.At(i)
	fn, ok := sel.Obj().(*types.Func)
	if !ok {
		return "", false
	}
	if pkgPath != "" && (fn.Pkg() == nil || fn.Pkg().Path() != pkgPath) {
		return "", false
	}
	if !isRuleMethodName(fn.Name()) {
		return "", false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || !ruleShapeSignature(sig) {
		return "", false
	}
	return fn.Name(), true
}

// isRuleMethodName returns true when name has one of the rule method prefixes:
// validate, checkDEP, checkCH.
func isRuleMethodName(name string) bool {
	return strings.HasPrefix(name, "validate") ||
		strings.HasPrefix(name, "checkDEP") ||
		strings.HasPrefix(name, "checkCH")
}

// ruleShapeSignature returns true when sig matches the rule shape:
// func() []ValidationResult (zero params, single []ValidationResult result).
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

// extractRegisteredFromAllRules scans the allRules package-level var and
// collects method names from each Rule's Detect field.
//
// Returns (registered, registryRel, "") on success and (registered, registryRel,
// fatalMsg) on shape error. registryRel is the module-relative path of the file
// declaring allRules (empty if no allRules var is found), used by the caller to
// anchor registration diagnostics.
func extractRegisteredFromAllRules(pkg *governancePackage) (map[string]struct{}, string, string) {
	registered := map[string]struct{}{}
	var registryRel string
	var fatal string

	for _, file := range pkg.files {
		if fatal != "" {
			break
		}
		relPath := pkg.fileRel(file)
		cl := findAllRulesCompositeLit(file)
		if cl == nil {
			continue
		}
		registryRel = relPath
		fatal = extractRegisteredFromRuleList(cl, relPath, pkg.fset, registered)
	}
	return registered, registryRel, fatal
}

// findAllRulesCompositeLit finds the composite literal value of the allRules
// package-level var in the given file, or returns nil if not found.
func findAllRulesCompositeLit(file *ast.File) *ast.CompositeLit {
	var result *ast.CompositeLit
	scanner.EachInChildren[ast.GenDecl](file, func(gd *ast.GenDecl) {
		if result != nil || gd.Tok != token.VAR {
			return
		}
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
		if ok {
			result = cl
		}
	})
	return result
}

// extractRegisteredFromRuleList walks a []Rule{...} composite literal and
// collects Detect method names into registered. Returns a fatal message on error.
func extractRegisteredFromRuleList(cl *ast.CompositeLit, relPath string, fset *token.FileSet, registered map[string]struct{}) string {
	var fatal string
	scanner.EachInChildren[ast.CompositeLit](cl, func(ruleLit *ast.CompositeLit) {
		if fatal != "" {
			return
		}
		fatal = extractRegisteredFromRuleLit(ruleLit, relPath, fset, registered)
	})
	return fatal
}

// extractRegisteredFromRuleLit extracts the Detect method name from one Rule
// composite literal and adds it to registered.
func extractRegisteredFromRuleLit(ruleLit *ast.CompositeLit, relPath string, fset *token.FileSet, registered map[string]struct{}) string {
	var fatal string
	scanner.EachInChildren[ast.KeyValueExpr](ruleLit, func(kv *ast.KeyValueExpr) {
		if fatal != "" {
			return
		}
		keyIdent, ok := kv.Key.(*ast.Ident)
		if !ok || keyIdent.Name != "Detect" {
			return
		}
		name, ok, isFatal := extractDetectMethodName(kv.Value, relPath, fset)
		if isFatal {
			fatal = name
			return
		}
		if ok {
			registered[name] = struct{}{}
		}
	})
	return fatal
}

// extractDetectMethodName extracts the method name from a Detect field value.
// Returns (methodName, true, false) for a method expression (*Validator).NAME.
// Returns ("", false, false) for a *ast.FuncLit (VERIFY-06 skip pattern).
// Returns (fatalMsg, false, true) for any unrecognized shape (fail-closed).
func extractDetectMethodName(expr ast.Expr, relPath string, fset *token.FileSet) (string, bool, bool) {
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		if name, matched := methodExprValidatorName(sel); matched {
			return name, true, false
		}
		pos := fset.Position(expr.Pos())
		msg := "unrecognized Detect shape at " + relPath + ":" + strconv.Itoa(pos.Line) +
			" — every allRules Detect must be (*Validator).methodName or a FuncLit closure"
		return msg, false, true
	}

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

	pos := fset.Position(expr.Pos())
	msg := "unrecognized Detect shape at " + relPath + ":" + strconv.Itoa(pos.Line) +
		" — every allRules Detect must be (*Validator).methodName or a FuncLit closure"
	return msg, false, true
}

// isVERIFY06ClosureShape reports whether fl is the exact VERIFY-06 FuncLit shape:
//
//	func(v *Validator) []ValidationResult { return v.validateVERIFY06(v.runCtx) }
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

// methodExprValidatorName returns the method name if sel is the structural
// pattern (*Validator).NAME, and false otherwise.
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

// ----- INV-2 helpers -----

// scanINV2ViolationsInFile reports all INV-2 violations in a single AST file.
// Two scan paths: (1) CallExpr emitter name-arg, (2) CompositeLit ValidationResult.
// Each diagnostic carries the structured (relPath, line) of the offending node so
// Report renders a clickable "<rel>:<line>:" prefix.
func scanINV2ViolationsInFile(
	file *ast.File,
	fset *token.FileSet,
	info *types.Info,
	ruleCodeConsts map[*types.Const]struct{},
	relPath string,
	pkgPath string,
) []Diagnostic {
	var diags []Diagnostic
	diags = append(diags, scanINV2CallExprs(file, fset, info, ruleCodeConsts, relPath)...)
	diags = append(diags, scanINV2CompositeLits(file, fset, info, ruleCodeConsts, relPath, pkgPath)...)
	return diags
}

// scanINV2CallExprs scans emitter CallExprs for bad code args.
func scanINV2CallExprs(
	file *ast.File,
	fset *token.FileSet,
	info *types.Info,
	ruleCodeConsts map[*types.Const]struct{},
	relPath string,
) []Diagnostic {
	var diags []Diagnostic
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		name := governanceEmitterName(call)
		if name == "" || len(call.Args) == 0 {
			return
		}
		codeArg := call.Args[0]
		if !ruleCodeArgShapeIsValid(codeArg) ||
			!ruleCodeArgResolvesToConst(codeArg, info, ruleCodeConsts) {
			pos := fset.Position(codeArg.Pos())
			diags = append(diags, diagAt(relPath, pos.Line,
				"code arg to "+name+" must be a RuleCode-typed const from rulecodes.go — "+
					"got AST shape "+astShapeName(codeArg)))
		}
	})
	return diags
}

// scanINV2CompositeLits scans ValidationResult{} composite literals for INV-2.
func scanINV2CompositeLits(
	file *ast.File,
	fset *token.FileSet,
	info *types.Info,
	ruleCodeConsts map[*types.Const]struct{},
	relPath string,
	pkgPath string,
) []Diagnostic {
	var diags []Diagnostic
	scanner.EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
		if !isValidationResultCompositeLit(cl, info, pkgPath) {
			return
		}
		diags = append(diags, checkINV2CompositeLit(cl, fset, info, ruleCodeConsts, relPath)...)
	})
	return diags
}

// checkINV2CompositeLit checks one ValidationResult{} composite literal for INV-2.
func checkINV2CompositeLit(
	cl *ast.CompositeLit,
	fset *token.FileSet,
	info *types.Info,
	ruleCodeConsts map[*types.Const]struct{},
	relPath string,
) []Diagnostic {
	// (a) Positional ban.
	keyValueCount := 0
	scanner.EachInChildren[ast.KeyValueExpr](cl, func(_ *ast.KeyValueExpr) {
		keyValueCount++
	})
	if len(cl.Elts) > 0 && keyValueCount != len(cl.Elts) {
		pos := fset.Position(cl.Pos())
		return []Diagnostic{diagAt(relPath, pos.Line,
			"ValidationResult literal must use named fields — positional element forbidden")}
	}
	// (b)(c) Code: presence and value.
	var codeValue ast.Expr
	scanner.EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
		key, ok := kv.Key.(*ast.Ident)
		if ok && key.Name == "Code" {
			codeValue = kv.Value
		}
	})
	if codeValue == nil {
		pos := fset.Position(cl.Pos())
		return []Diagnostic{diagAt(relPath, pos.Line,
			"ValidationResult literal omits Code: field — every result must reference a RuleCode const from rulecodes.go")}
	}
	if !ruleCodeArgShapeIsValid(codeValue) ||
		!ruleCodeArgResolvesToConst(codeValue, info, ruleCodeConsts) {
		pos := fset.Position(codeValue.Pos())
		return []Diagnostic{diagAt(relPath, pos.Line,
			"ValidationResult.Code must be a RuleCode-typed const from rulecodes.go — got AST shape "+astShapeName(codeValue))}
	}
	return nil
}

// ruleCodeArgShapeIsValid reports whether expr is an *ast.Ident (INV-2 code-arg shape).
func ruleCodeArgShapeIsValid(expr ast.Expr) bool {
	_, ok := expr.(*ast.Ident)
	return ok
}

// ruleCodeArgResolvesToConst returns true when expr is an *ast.Ident that
// resolves (via info.Uses) to a *types.Const in ruleCodeConsts.
func ruleCodeArgResolvesToConst(expr ast.Expr, info *types.Info, ruleCodeConsts map[*types.Const]struct{}) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	if info == nil {
		return false // fail-closed: cannot confirm const identity without type info
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
// governance package scope that have type RuleCode and are declared in rulecodes.go.
func collectRuleCodeConsts(pkg *governancePackage) map[*types.Const]struct{} {
	out := map[*types.Const]struct{}{}
	for _, name := range pkg.scope.Names() {
		obj := pkg.scope.Lookup(name)
		c, ok := obj.(*types.Const)
		if !ok {
			continue
		}
		named, ok := c.Type().(*types.Named)
		if !ok || named.Obj().Name() != "RuleCode" {
			continue
		}
		if named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != governancePkgPath {
			continue
		}
		pos := pkg.fset.Position(c.Pos())
		if filepath.Base(pos.Filename) != ruleCodesFile {
			continue
		}
		out[c] = struct{}{}
	}
	return out
}

// isValidationResultCompositeLit returns true when cl is typed as
// kernel/governance.ValidationResult.
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

// governanceEmitterName returns the governance emitter constructor name a
// CallExpr targets, or "" if the call is not a constructor.
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

// ----- INV-3 helpers -----

// printfVerbRe matches a Go printf format verb.
var printfVerbRe = regexp.MustCompile(`%[#+\- 0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z%]`)

// fixHasLiteralContent reports whether a resolved fix string carries actual
// remediation text — at least one non-whitespace rune that is not a printf verb.
func fixHasLiteralContent(resolved string) bool {
	return strings.TrimSpace(printfVerbRe.ReplaceAllString(resolved, "")) != ""
}

// scanFixFieldViolationsInFile reports all INV-3 violations in a single AST file.
// Each diagnostic carries the structured (relPath, line) of the offending node so
// Report renders a clickable "<rel>:<line>:" prefix.
func scanFixFieldViolationsInFile(
	file *ast.File,
	fset *token.FileSet,
	info *types.Info,
	consts map[string]string,
	relPath string,
	pkgPath string,
) []Diagnostic {
	var diags []Diagnostic
	diags = append(diags, scanINV3FixArgs(file, fset, consts, info, relPath)...)
	if filepath.Base(relPath) == "locator.go" {
		return diags
	}
	diags = append(diags, scanINV3CompositeLits(file, fset, info, relPath, pkgPath)...)
	return diags
}

// scanINV3FixArgs scans emitter calls for empty/unresolvable fix args.
func scanINV3FixArgs(
	file *ast.File,
	fset *token.FileSet,
	consts map[string]string,
	info *types.Info,
	relPath string,
) []Diagnostic {
	var diags []Diagnostic
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
		diags = append(diags, diagAt(relPath, pos.Line,
			name+" fix argument is empty, unresolvable, or carries no literal guidance — "+
				"every finding must carry remediation text in the typed Fix field (GOVERNANCE-RULE-ERROR-FIX-FIELD-01)"))
	})
	return diags
}

// scanINV3CompositeLits scans for raw ValidationResult{} literals outside locator.go.
func scanINV3CompositeLits(
	file *ast.File,
	fset *token.FileSet,
	info *types.Info,
	relPath string,
	pkgPath string,
) []Diagnostic {
	var diags []Diagnostic
	scanner.EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
		if !isValidationResultCompositeLit(cl, info, pkgPath) {
			return
		}
		pos := fset.Position(cl.Pos())
		diags = append(diags, diagAt(relPath, pos.Line,
			"raw ValidationResult{} literal is forbidden outside locator.go — construct findings "+
				"via newError / newWarning / newErrorAt / newScopedError (GOVERNANCE-RULE-ERROR-FIX-FIELD-01)"))
	})
	return diags
}

// resolveStringFragments returns every string fragment that contributes to
// expr's string value.
func resolveStringFragments(expr ast.Expr, consts map[string]string, info *types.Info) []string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return resolveBasicLitFragment(e)
	case *ast.Ident:
		return resolveIdentFragment(e, consts, info)
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

// resolveBasicLitFragment returns the unquoted string value of a STRING literal,
// or nil for non-string literal kinds or malformed literals.
func resolveBasicLitFragment(e *ast.BasicLit) []string {
	if e.Kind != token.STRING {
		return nil
	}
	v, err := strconv.Unquote(e.Value)
	if err != nil {
		return nil
	}
	return []string{v}
}

// resolveIdentFragment resolves an identifier to its string value by first
// checking the local consts map, then falling back to EvaluateConstString.
func resolveIdentFragment(e *ast.Ident, consts map[string]string, info *types.Info) []string {
	if v, ok := consts[e.Name]; ok {
		return []string{v}
	}
	if v, ok := EvaluateConstString(info, e); ok {
		return []string{v}
	}
	return nil
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

// collectPackageStringConsts walks scope's names and returns a map from every
// package-scope string const name to its value.
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

// findTypesPackageByPath performs a depth-first search through pkg's transitive
// *types.Package import graph to find the package with the given import path.
// Returns nil if pkg is nil or the path is not found.
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

// ----- INV-4 helpers -----

// allRulesEntry holds the Code string value and Detect method name extracted
// from a single Rule composite literal in allRules, plus the (rel, line) of that
// Rule literal so binding diagnostics anchor to the registration site.
type allRulesEntry struct {
	codeValue  string // resolved string value of the Code RuleCode const
	methodName string // name of the detect method
	rel        string // module-relative path of the file declaring the Rule literal
	line       int    // 1-based line of the Rule composite literal
}

// extractAllRulesEntries parses each element of the allRules composite literal
// and returns one allRulesEntry per Rule element.
//
// Returns (entries, registryRel, "") on success and (nil, registryRel, fatalMsg)
// when a fatal shape is encountered. registryRel is the module-relative path of
// the file declaring allRules (empty if none is found).
func extractAllRulesEntries(pkg *governancePackage) ([]allRulesEntry, string, string) {
	var entries []allRulesEntry
	var registryRel string
	var fatal string

	for _, file := range pkg.files {
		if fatal != "" {
			break
		}
		relPath := pkg.fileRel(file)
		cl := findAllRulesCompositeLit(file)
		if cl == nil {
			continue
		}
		registryRel = relPath
		fileEntries, fileFatal := parseAllRulesLit(cl, relPath, pkg)
		if fileFatal != "" {
			fatal = fileFatal
			break
		}
		entries = append(entries, fileEntries...)
	}
	return entries, registryRel, fatal
}

// parseAllRulesLit walks the allRules composite literal and returns entries
// parsed from each Rule element.
func parseAllRulesLit(cl *ast.CompositeLit, relPath string, pkg *governancePackage) ([]allRulesEntry, string) {
	var entries []allRulesEntry
	var fatal string
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
	return entries, fatal
}

// parseRuleEntry extracts the Code and Detect values from a single Rule
// composite literal element of allRules.
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

	codeValue, ok := resolveRuleCodeValue(codeExpr, pkg.info)
	if !ok {
		pos := pkg.fset.Position(codeExpr.Pos())
		return nil, "cannot resolve Code value at " + relPath + ":" + strconv.Itoa(pos.Line) +
			" — Code must be an Ident referencing a RuleCode const"
	}

	methodName, found, isFatal := extractDetectMethodName(detectExpr, relPath, pkg.fset)
	if isFatal {
		return nil, methodName
	}
	if !found {
		if fl, ok := detectExpr.(*ast.FuncLit); ok && isVERIFY06ClosureShape(fl) {
			methodName = "validateVERIFY06"
		} else {
			return nil, ""
		}
	}

	pos := pkg.fset.Position(ruleLit.Pos())
	return &allRulesEntry{codeValue: codeValue, methodName: methodName, rel: relPath, line: pos.Line}, ""
}

// resolveRuleCodeValue extracts the string value of a RuleCode const ident.
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
// all methods with receiver *Validator in the given AST files.
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
// starting from startMethod, collecting RuleCode string values emitted by
// newError/newWarning/newScopedError/newErrorAt.
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
		collectEmittedCodesFromCall(call, methodMap, ruleCodeConsts, info, emitted, queue)
	})
}

// collectEmittedCodesFromCall processes one CallExpr: collects emitted codes or
// enqueues BFS callee.
func collectEmittedCodesFromCall(
	call *ast.CallExpr,
	methodMap map[string]*ast.FuncDecl,
	ruleCodeConsts map[*types.Const]struct{},
	info *types.Info,
	emitted map[string]struct{},
	queue *[]string,
) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		collectEmittedFromIdent(call, ruleCodeConsts, info, emitted)
		return
	}
	if sel.Sel == nil {
		return
	}
	callee := sel.Sel.Name
	if isGovernanceEmitterName(callee) && len(call.Args) > 0 {
		if v, resolved := resolveRuleCodeArg(call.Args[0], info, ruleCodeConsts); resolved {
			emitted[v] = struct{}{}
		}
		return
	}
	if _, inMap := methodMap[callee]; inMap {
		*queue = append(*queue, callee)
	}
}

// collectEmittedFromIdent handles the plain-Ident (non-selector) CallExpr branch:
// if the callee name is a governance emitter and args are present, collects the code.
func collectEmittedFromIdent(
	call *ast.CallExpr,
	ruleCodeConsts map[*types.Const]struct{},
	info *types.Info,
	emitted map[string]struct{},
) {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return
	}
	if !isGovernanceEmitterName(ident.Name) || len(call.Args) == 0 {
		return
	}
	if v, resolved := resolveRuleCodeArg(call.Args[0], info, ruleCodeConsts); resolved {
		emitted[v] = struct{}{}
	}
}

// resolveRuleCodeArg resolves expr to its RuleCode string value.
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
	if len(ruleCodeConsts) > 0 {
		if _, found := ruleCodeConsts[c]; !found {
			return "", false
		}
	}
	v, ok := EvaluateConstString(info, ident)
	return v, ok
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
		if !ok || named.Obj().Name() != "RuleCode" {
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
	// sort inline to avoid import of "sort" — use simple insertion sort for
	// small sets (governance rule codes, typically < 100 elements).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// ----- INV-5 helpers -----

// scanEmitterFuncValueUsages returns the positions of references to the four
// governance emitter constructors that are NOT the direct callee of a call
// expression (func-value indirections).
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
	scanner.EachInChildren[ast.FuncDecl](f, func(x *ast.FuncDecl) {
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
