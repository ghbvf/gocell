// invariants asserted in this file:
//   - INVARIANT: FIXTURE-CELLID-TYPED-BUILDER-01
//   - INVARIANT: METADATATEST-IMPORT-SCOPE-01
//
// This _test.go only dogfoods + precision-gates the two rules. The importable
// rule bodies (Check* entry points, all detection/scanner helpers, consts,
// types, vars) live in the non-test companion
// fixture_cellid_typed_builder.go (M3 #1639).
//
// Self-checks kept here:
//   - A2 TestFixtureCellIDTypedBuilder_NewCellIDBodyShape — locks the body of
//     metadatatest.NewCellID via TypesInfo (panicregister.Approved + errcode.Assertion).
//   - A3 TestFixtureCellIDTypedBuilder_NegativeFixture — RED fixture; asserts
//     A1 fires on deliberate bad cases and does not fire on good ones.
//   - A4 TestFixtureCellIDTypedBuilder_CarveOutADRConsistency — ADR carveout
//     registry ↔ fixtureCellIDCarveOuts map must be character-identical.
//   - A5 TestFixtureCellIDTypedBuilder_VarInitializerShape — locks that every
//     CellID-prefixed var in metadatatest is initialized via NewCellID(literal).
//
// ref: tools/archtest/cell_id_pattern_single_source_test.go — sibling
//
//	typeseval funnel (Medium; PR #484).
//
// ref: pkg/panicregister/panicregister.go — Approved funnel range.
// ref: kernel/metadata/metadatatest/cellid.go — typed builder body.
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestFixtureCellIDTypedBuilder is the A1 dogfood — delegates entirely to the
// importable rule body in the companion .go.
func TestFixtureCellIDTypedBuilder(t *testing.T) {
	t.Parallel()
	Report(t, fixtureCellIDRuleID, CheckFixtureCellIDTypedBuilder(t, ConfigForExternalCell{}))
}

// TestMetadatatestImportScope is the METADATATEST-IMPORT-SCOPE-01 dogfood —
// delegates entirely to the importable rule body in the companion .go.
func TestMetadatatestImportScope(t *testing.T) {
	t.Parallel()
	Report(t, metadatatestImportScopeRuleID, CheckMetadatatestImportScope(t, ConfigForExternalCell{}))
}

// TestFixtureCellIDTypedBuilder_NewCellIDBodyShape (A2) locks the shape
// of metadatatest.NewCellID. Body must be exactly:
//
//	if !metadata.MatchCellID(s) {
//	    panic(panicregister.Approved("metadatatest-cell-id-invalid", errcode.Assertion(...)))
//	}
//	return s
func TestFixtureCellIDTypedBuilder_NewCellIDBodyShape(t *testing.T) {
	t.Parallel()

	// A2 loads only the metadatatest package — no need to pull in the entire
	// module type-graph. Run(t, Typed(...)) with a single-package pattern is faster and
	// matches the "narrow scope" guidance in ai-robust.md §载体决策原则.
	type a2Result struct {
		fn    *ast.FuncDecl
		pInfo *types.Info
	}
	var result a2Result
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./kernel/metadata/metadatatest/..."}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != metadatatestPkgPath {
			return nil
		}
		for _, file := range p.Files {
			if filepath.Base(p.Abs(file)) != "cellid.go" {
				continue
			}
			EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				if result.fn != nil || fd.Name == nil || fd.Name.Name != metadatatestNewCellIDFunc {
					return
				}
				result.fn = fd
				result.pInfo = p.TypesInfo
			})
		}
		return nil
	})

	fn := result.fn
	pInfo := result.pInfo
	if fn == nil {
		t.Fatalf("%s/A2: metadatatest.NewCellID FuncDecl not found", fixtureCellIDRuleID)
	}
	if fn.Body == nil || len(fn.Body.List) != 2 {
		t.Fatalf("%s/A2: NewCellID body must have exactly 2 statements (if-panic + return), got %d",
			fixtureCellIDRuleID, len(fn.Body.List))
	}
	ifStmt, ok := fn.Body.List[0].(*ast.IfStmt)
	if !ok {
		t.Fatalf("%s/A2: first statement must be *ast.IfStmt, got %T", fixtureCellIDRuleID, fn.Body.List[0])
	}
	if !isMatchCellIDNegateCond(pInfo, ifStmt.Cond) {
		t.Fatalf("%s/A2: if condition must be !metadata.MatchCellID(s) resolved via TypesInfo to %s.MatchCellID",
			fixtureCellIDRuleID, metadataPkgPath)
	}
	if ifStmt.Else != nil {
		t.Fatalf("%s/A2: if statement must have no else branch", fixtureCellIDRuleID)
	}
	if ifStmt.Body == nil || len(ifStmt.Body.List) != 1 {
		t.Fatalf("%s/A2: if body must contain exactly one statement (panic)", fixtureCellIDRuleID)
	}
	exprStmt, ok := ifStmt.Body.List[0].(*ast.ExprStmt)
	if !ok {
		t.Fatalf("%s/A2: if body statement must be *ast.ExprStmt, got %T", fixtureCellIDRuleID, ifStmt.Body.List[0])
	}
	panicCall, ok := exprStmt.X.(*ast.CallExpr)
	if !ok {
		t.Fatalf("%s/A2: if body must be panic(...) call", fixtureCellIDRuleID)
	}
	panicIdent, ok := panicCall.Fun.(*ast.Ident)
	if !ok || panicIdent.Name != "panic" || len(panicCall.Args) != 1 {
		t.Fatalf("%s/A2: must be bare panic(arg) with one arg", fixtureCellIDRuleID)
	}
	approvedCall, ok := panicCall.Args[0].(*ast.CallExpr)
	if !ok {
		t.Fatalf("%s/A2: panic arg must be panicregister.Approved(...) CallExpr", fixtureCellIDRuleID)
	}
	approvedSel, ok := approvedCall.Fun.(*ast.SelectorExpr)
	if !ok || approvedSel.Sel.Name != "Approved" {
		t.Fatalf("%s/A2: panic arg must be a SelectorExpr ending in .Approved", fixtureCellIDRuleID)
	}
	// F1: verify Approved resolves to panicregister.Approved via TypesInfo — a
	// SelectorExpr with .Name=="Approved" is not sufficient; a homonymous function
	// in another package would silently slip through. Package-path lock is Hard.
	// Derived from PlatformModulePath to satisfy ARCHTEST-MODULE-PATH-FUNNEL-01.
	if pInfo != nil {
		approvedObj := pInfo.Uses[approvedSel.Sel]
		approvedFn, isFn := approvedObj.(*types.Func)
		const panicregPkgPath = PlatformModulePath + "/pkg/panicregister"
		if !isFn || approvedFn.Pkg() == nil || approvedFn.Pkg().Path() != panicregPkgPath || approvedFn.Name() != "Approved" {
			t.Fatalf("%s/A2: Approved must resolve to %s.Approved, got %v", fixtureCellIDRuleID, panicregPkgPath, approvedObj)
		}
	}
	if len(approvedCall.Args) != 2 {
		t.Fatalf("%s/A2: panicregister.Approved must take 2 args", fixtureCellIDRuleID)
	}
	reasonLit, ok := approvedCall.Args[0].(*ast.BasicLit)
	if !ok || reasonLit.Kind != token.STRING {
		t.Fatalf("%s/A2: first arg to Approved must be a string literal", fixtureCellIDRuleID)
	}
	got, err := strconv.Unquote(reasonLit.Value)
	if err != nil || got != "metadatatest-cell-id-invalid" {
		t.Fatalf("%s/A2: Approved reason must be %q, got %s", fixtureCellIDRuleID, "metadatatest-cell-id-invalid", reasonLit.Value)
	}
	assertCall, ok := approvedCall.Args[1].(*ast.CallExpr)
	if !ok {
		t.Fatalf("%s/A2: second arg to Approved must be errcode.Assertion(...) CallExpr", fixtureCellIDRuleID)
	}
	assertSel, ok := assertCall.Fun.(*ast.SelectorExpr)
	if !ok || assertSel.Sel.Name != "Assertion" {
		t.Fatalf("%s/A2: second arg must be a SelectorExpr ending in .Assertion", fixtureCellIDRuleID)
	}
	// F1: verify Assertion resolves to errcode.Assertion via TypesInfo.
	// Derived from PlatformModulePath to satisfy ARCHTEST-MODULE-PATH-FUNNEL-01.
	if pInfo != nil {
		assertObj := pInfo.Uses[assertSel.Sel]
		assertFn, isFn := assertObj.(*types.Func)
		const errcodePkgPath = PlatformModulePath + "/pkg/errcode"
		if !isFn || assertFn.Pkg() == nil || assertFn.Pkg().Path() != errcodePkgPath || assertFn.Name() != "Assertion" {
			t.Fatalf("%s/A2: Assertion must resolve to %s.Assertion, got %v", fixtureCellIDRuleID, errcodePkgPath, assertObj)
		}
	}
	retStmt, ok := fn.Body.List[1].(*ast.ReturnStmt)
	if !ok || len(retStmt.Results) != 1 {
		t.Fatalf("%s/A2: second statement must be 'return s'", fixtureCellIDRuleID)
	}
	retIdent, ok := retStmt.Results[0].(*ast.Ident)
	if !ok || retIdent.Name != "s" {
		t.Fatalf("%s/A2: must return identifier s", fixtureCellIDRuleID)
	}
}

// TestFixtureCellIDTypedBuilder_VarInitializerShape (A5) locks the body-form
// of every CellID-prefixed package-level var in kernel/metadata/metadatatest.
// Each such var must have its initializer be exactly
// metadatatest.NewCellID(BasicLit STRING) — same TypesInfo-resolved identity
// as the F1 lock on A2's body shape. This closes the upstream half of the
// CellID* var funnel: A1's SelectorExpr branch accepts any metadatatest var
// whose name starts with "CellID", and A5 guarantees that every such var was
// constructed via the sanctioned NewCellID(literal) path. Together A5
// (upstream) + A1 SelectorExpr branch (downstream) form a Hard funnel —
// adding "var CellIDBypass = "raw-evil"" would fail A5 immediately.
//
// Symmetric to A2 (NewCellID body-shape lock). A2 protects the constructor;
// A5 protects every site that uses the constructor at package init.
func TestFixtureCellIDTypedBuilder_VarInitializerShape(t *testing.T) {
	t.Parallel()

	type result struct {
		pInfo *types.Info
		specs []*ast.ValueSpec
	}
	var collected result
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./kernel/metadata/metadatatest/..."}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != metadatatestPkgPath {
			return nil
		}
		collected.pInfo = p.TypesInfo
		for _, file := range p.Files {
			if filepath.Base(p.Abs(file)) != "cellid.go" {
				continue
			}
			EachInChildren[ast.GenDecl](file, func(gd *ast.GenDecl) {
				if gd.Tok != token.VAR {
					return
				}

				EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
					collected.specs = append(collected.specs, vs)
				})
			})
		}
		return nil
	})

	if len(collected.specs) == 0 {
		t.Fatalf("%s/A5: no var GenDecl found in kernel/metadata/metadatatest/cellid.go", fixtureCellIDRuleID)
	}
	if collected.pInfo == nil {
		t.Fatalf("%s/A5: TypesInfo not captured", fixtureCellIDRuleID)
	}

	for _, vs := range collected.specs {
		for i, name := range vs.Names {
			if !strings.HasPrefix(name.Name, "CellID") {
				continue
			}
			if i >= len(vs.Values) {
				t.Errorf("%s/A5: %s has no initializer (must be NewCellID(literal))", fixtureCellIDRuleID, name.Name)
				continue
			}
			call, ok := vs.Values[i].(*ast.CallExpr)
			if !ok {
				t.Errorf("%s/A5: %s initializer must be NewCellID(BasicLit STRING) CallExpr, got %T", fixtureCellIDRuleID, name.Name, vs.Values[i])
				continue
			}
			// Resolve the callee via TypesInfo — Sel.Name match alone is Soft.
			var callee *types.Func
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				obj := collected.pInfo.Uses[fn]
				if f, isFn := obj.(*types.Func); isFn {
					callee = f
				}
			case *ast.SelectorExpr:
				obj := collected.pInfo.Uses[fn.Sel]
				if f, isFn := obj.(*types.Func); isFn {
					callee = f
				}
			}
			if callee == nil || callee.Pkg() == nil ||
				callee.Pkg().Path() != metadatatestPkgPath || callee.Name() != metadatatestNewCellIDFunc {
				t.Errorf("%s/A5: %s initializer must call %s.%s, got %v",
					fixtureCellIDRuleID, name.Name, metadatatestPkgPath, metadatatestNewCellIDFunc, callee)
				continue
			}
			if len(call.Args) != 1 {
				t.Errorf("%s/A5: %s NewCellID call must take exactly 1 argument, got %d", fixtureCellIDRuleID, name.Name, len(call.Args))
				continue
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s/A5: %s NewCellID argument must be a string BasicLit, got %T", fixtureCellIDRuleID, name.Name, call.Args[0])
			}
		}
	}
}

// TestFixtureCellIDTypedBuilder_NegativeFixture (A3) loads the
// archtest_fixture sub-package fixturecellidnegfixture/, which contains
// deliberate bad and good usages. The bad usages must be reported by
// A1's scanner; good usages must not.
func TestFixtureCellIDTypedBuilder_NegativeFixture(t *testing.T) {
	t.Parallel()

	allowSelfFile := map[string]struct{}{} // no allow in fixture scope
	carveOuts := map[string]struct{}{}     // no carveouts in fixture scope

	var violations []string
	visitedFiles := make(map[string]struct{})
	fixturePkgPattern := []string{"./tools/archtest/internal/fixturecellidnegfixture"}
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, fixturePkgPattern), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			visitedFiles[filepath.Base(rel)] = struct{}{}
			if _, ok := allowSelfFile[rel]; ok {
				continue
			}
			carved := carvedOutFunctions(file, p, carveOuts)
			EachInSubtree[ast.CompositeLit](file, func(comp *ast.CompositeLit) {
				if isInsideCarvedFunc(comp, carved) {
					return
				}
				violations = append(violations, scanCellIDComposite(p, file, rel, comp)...)
			})
		}
		return nil
	})

	sort.Strings(violations)
	violations = dedupSortedStrings(violations)

	const negFixturePrefix = "tools/archtest/internal/fixturecellidnegfixture/"

	// hasFile reports whether any violation string contains an exact
	// rel-path segment for the given filename, e.g.
	// "tools/archtest/internal/fixturecellidnegfixture/bad_map_key.go:N:M:".
	hasFile := func(filename string) bool {
		prefix := negFixturePrefix + filename + ":"
		for _, v := range violations {
			if strings.HasPrefix(strings.TrimLeft(v, " "), prefix) {
				return true
			}
		}
		return false
	}

	// Each bad file emits at least one finding.
	wantBadFiles := []string{
		"bad_map_key.go",
		"bad_field.go",
		"bad_slice_elem.go",
		"bad_ident_chain.go",
		"bad_slice_belongs.go",
		"bad_contract_owner.go",
		"bad_endpoints_server.go",
		"bad_endpoints_slices.go",
		"bad_assembly_cells.go",
		// F5 reverse fixtures: NewCellID(var) and non-CellID-prefixed local var ref.
		"bad_dynamic_arg.go",
		"bad_unsanctioned_var.go",
	}
	var missing []string
	for _, want := range wantBadFiles {
		if !hasFile(want) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%s/A3: archtest A1 failed to detect bad fixtures in %v.\nViolations seen:\n  %s",
			fixtureCellIDRuleID, missing, strings.Join(violations, "\n  "))
	}

	// bad_ident_chain.go uses localBareCellID at both map key AND
	// CellMeta.ID — must produce at least 2 findings.
	var identChainCount int
	for _, v := range violations {
		if strings.HasPrefix(strings.TrimLeft(v, " "), negFixturePrefix+"bad_ident_chain.go:") {
			identChainCount++
		}
	}
	if identChainCount < 2 {
		t.Errorf("%s/A3: bad_ident_chain.go should produce ≥ 2 findings (map key + CellMeta.ID), got %d",
			fixtureCellIDRuleID, identChainCount)
	}

	// Good files must NOT produce any violations.
	goodFiles := []string{"good_const_ref.go", "good_call_literal.go"}
	for _, v := range violations {
		for _, gf := range goodFiles {
			if strings.HasPrefix(strings.TrimLeft(v, " "), negFixturePrefix+gf+":") {
				t.Errorf("%s/A3: archtest A1 produced false positive on good fixture: %s", fixtureCellIDRuleID, v)
			}
		}
	}

	// Blind-spot files must NOT produce any violations (they document
	// known A1 limitations, not bugs).
	blindSpotFiles := []string{"blind_spot_ident_slice.go", "blind_spot_assign.go"}
	for _, v := range violations {
		for _, bf := range blindSpotFiles {
			if strings.HasPrefix(strings.TrimLeft(v, " "), negFixturePrefix+bf+":") {
				t.Errorf("%s/A3: archtest A1 produced unexpected violation on blind-spot fixture (known A1 limitation): %s", fixtureCellIDRuleID, v)
			}
		}
	}

	// Verify that good, blind-spot, and new bad fixture files were actually
	// loaded by Run(t, Fixture(...)). If a file is absent (e.g. build-tag mismatch
	// or path error), the assertions above silently pass because there is
	// nothing to check — a false positive on success.
	wantVisited := []string{
		"good_const_ref.go",
		"good_call_literal.go",
		"blind_spot_ident_slice.go",
		"blind_spot_assign.go",
		"bad_dynamic_arg.go",
		"bad_unsanctioned_var.go",
	}
	for _, want := range wantVisited {
		if _, ok := visitedFiles[want]; !ok {
			t.Errorf("%s/A3: expected fixture file %s was not loaded by Run(t, Fixture(...)) (build-tag or path issue?)", fixtureCellIDRuleID, want)
		}
	}
}

// TestFixtureCellIDTypedBuilder_CarveOutADRConsistency (A4) parses the
// carveout registry table in the ADR and the fixtureCellIDCarveOuts map
// in this file, asserting both sides list character-identical function
// qualified names. Mirrors ERRCODE-CARVEOUT-ADR-CONSISTENCY-01.
func TestFixtureCellIDTypedBuilder_CarveOutADRConsistency(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	adrPath := filepath.Join(root, fixtureCellIDADRFile)
	adrBytes, err := os.ReadFile(adrPath) //nolint:gosec // file path is a known compile-time constant rooted at module dir
	if err != nil {
		t.Fatalf("%s/A4: read ADR %s: %v", fixtureCellIDRuleID, fixtureCellIDADRFile, err)
	}
	adrSet, err := parseCarveOutTableFromADR(string(adrBytes))
	if err != nil {
		t.Fatalf("%s/A4: parse ADR carveout table: %v", fixtureCellIDRuleID, err)
	}

	codeSet := make(map[string]struct{}, len(fixtureCellIDCarveOuts))
	for k := range fixtureCellIDCarveOuts {
		codeSet[k] = struct{}{}
	}

	// Equality: ADR set == code set.
	var onlyInADR, onlyInCode []string
	for k := range adrSet {
		if _, ok := codeSet[k]; !ok {
			onlyInADR = append(onlyInADR, k)
		}
	}
	for k := range codeSet {
		if _, ok := adrSet[k]; !ok {
			onlyInCode = append(onlyInCode, k)
		}
	}
	sort.Strings(onlyInADR)
	sort.Strings(onlyInCode)
	if len(onlyInADR)+len(onlyInCode) > 0 {
		t.Fatalf("%s/A4: carveout map ↔ ADR registry drift.\nOnly in ADR:\n  %s\nOnly in code (%s):\n  %s",
			fixtureCellIDRuleID,
			strings.Join(onlyInADR, "\n  "),
			"fixtureCellIDCarveOuts",
			strings.Join(onlyInCode, "\n  "))
	}
}
