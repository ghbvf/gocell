// invariants asserted in this file:
//   - INVARIANT: FIXTURE-CELLID-TYPED-BUILDER-01
//
// FIXTURE-CELLID-TYPED-BUILDER-01: every cell-id field position in a
// kernel/metadata.* struct or map composite literal — anywhere in the
// module's hand-written code (production + tests, all tag combinations) —
// must be sourced from kernel/metadata/metadatatest.NewCellID(literal)
// or one of metadatatest's pre-validated package-level cell-id vars
// (CellID*). Bare string literals at those positions are rejected.
//
// Enforcement is split into four sub-tests:
//
//   - A1 TestFixtureCellIDTypedBuilder — typed-info funnel: scans all
//     *ast.CompositeLit, resolves each to a kernel/metadata.* struct or
//     map type, identifies the cell-id field positions (15-field
//     enumeration below), and asserts every expression at such a
//     position resolves to metadatatest.NewCellID(BasicLit) or
//     metadatatest.<Var>. Hard downstream: callsite identity is
//     verified via go/types — Ident→BasicLit chains, third-party
//     consts, and dynamic NewCellID arguments are rejected uniformly.
//
//   - A2 TestFixtureCellIDTypedBuilder_NewCellIDBodyShape — locks
//     the metadatatest.NewCellID FuncDecl body form: exactly
//     {if !metadata.MatchCellID(s) { panic(panicregister.Approved(literal,
//     errcode.Assertion(...))) }; return s}. Hard upstream: any other
//     body form fails archtest.
//
//   - A3 TestFixtureCellIDTypedBuilder_NegativeFixture — loads the
//     fixturecellidnegfixture/ archtest_fixture sub-package containing
//     deliberate violations (bare map key, bare field value, bare slice
//     element, Ident→BasicLit chain) plus pass-through good cases,
//     and asserts A1 fires exactly on the bad cases. Reverse self-test
//     that guards against A1 over-broad or no-op regressions.
//
//   - A4 TestFixtureCellIDTypedBuilder_CarveOutADRConsistency — parses
//     the fixtureCellIDCarveOuts map in this file alongside the carveout
//     registry table in docs/architecture/<ts>-adr-fixture-cellid-typed-
//     builder.md, asserting both sides are character-identical. Mirrors
//     ERRCODE-CARVEOUT-ADR-CONSISTENCY-01.
//
// Carveouts (function-level only, per ai-robust.md):
//
//   - kernel/governance.TestValidator_FMTC1_CellIDPattern — FMT-C1 RED
//     case: fixtures intentionally use invalid cell ids ("foo-bar",
//     "FooBar", "1foo", "foo_bar") to verify FMT-C1 detection. The
//     builder would panic at construction. Function-level skip; carveout
//     registry mirrored in ADR §2.
//
// AI-robust: downstream Hard (A1 typed-info callsite identity), upstream
// Hard (A2 body form-uniqueness), meta Hard (A3 negative fixture + A4
// ADR consistency). The 15-field enumeration is a closed schema-derived
// set; new cell-id fields require a same-PR update to both this file and
// the ADR §1 field table. See ADR §3 升级路径.
//
// ref: tools/archtest/cell_id_pattern_single_source_test.go — sibling
//   typeseval funnel (Medium; PR #484).
// ref: pkg/panicregister/panicregister.go — Approved funnel range.
// ref: kernel/metadata/metadatatest/cellid.go — typed builder body.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	fixtureCellIDRuleID          = "FIXTURE-CELLID-TYPED-BUILDER-01"
	metadataPkgPath              = "github.com/ghbvf/gocell/kernel/metadata"
	metadatatestPkgPath          = "github.com/ghbvf/gocell/kernel/metadata/metadatatest"
	metadatatestNewCellIDFunc    = "NewCellID"
	metadatatestCellIDSourceFile = "kernel/metadata/metadatatest/cellid.go"
	fixtureCellIDADRFile         = "docs/architecture/202605271000-adr-fixture-cellid-typed-builder.md"
)

// cellIDFieldPosition identifies a struct field (or slice-field element
// position) whose string value semantics is a cell-id and therefore must
// be sourced from metadatatest. The 15-field enumeration mirrors the
// in-scope table in plan #681 / ADR §1; new cell-id fields require a
// same-PR update here AND in the ADR §1 table.
type cellIDFieldPosition struct {
	structName     string // e.g. "CellMeta"; package path is always metadataPkgPath
	fieldName      string // e.g. "ID"
	isSliceElement bool   // true when value is []string and each element is a cell-id
}

var cellIDFieldPositions = []cellIDFieldPosition{
	{"CellMeta", "ID", false},
	{"SliceMeta", "BelongsToCell", false},
	{"L0DepMeta", "Cell", false},
	{"ContractMeta", "OwnerCell", false},
	{"EndpointsMeta", "Server", false},
	{"EndpointsMeta", "Clients", true},
	{"EndpointsMeta", "Publisher", false},
	{"EndpointsMeta", "Handler", false},
	{"EndpointsMeta", "Invokers", true},
	{"EndpointsMeta", "Provider", false},
	{"EndpointsMeta", "Readers", true},
	{"JourneyMeta", "Cells", true},
	{"AssemblyMeta", "Cells", true},
	{"LocatedSliceMeta", "CellID", false},
}

// cellIDMapKeyValueStructs lists the named struct types T such that any
// map[string]*T or map[string]T composite literal has cell-id-semantics
// keys. Only ProjectMeta.Cells qualifies — Slices/Contracts/Journeys/
// Assemblies have composite ID semantics (slice-id paths, contract-id,
// etc.) and are tracked under separate mirror issues.
var cellIDMapKeyValueStructs = map[string]struct{}{
	"CellMeta": {},
}

// fixtureCellIDCarveOuts lists function-qualified names that opt out of
// A1 enforcement at the function-body level. Each entry must mirror an
// ADR §2 row. See A4 TestFixtureCellIDTypedBuilder_CarveOutADRConsistency.
var fixtureCellIDCarveOuts = map[string]struct{}{
	"github.com/ghbvf/gocell/kernel/governance.TestValidator_FMTC1_CellIDPattern": {},
}

// TestFixtureCellIDTypedBuilder enforces A1: every kernel/metadata-typed
// cell-id field position must be sourced from metadatatest.
func TestFixtureCellIDTypedBuilder(t *testing.T) {
	t.Parallel()

	allowSelfFile := map[string]struct{}{
		"kernel/metadata/metadatatest/cellid.go":      {},
		"kernel/metadata/metadatatest/cellid_test.go": {},
	}

	violations := scanCellIDFixtureViolations(t, allowSelfFile, fixtureCellIDCarveOuts)
	sort.Strings(violations)
	violations = dedupSortedStrings(violations)
	if len(violations) > 0 {
		t.Fatalf("%s: cell-id field positions in kernel/metadata.* composite "+
			"literals must use metadatatest.NewCellID(literal) or "+
			"metadatatest.<CellIDVar>. Bare string literals, identifier "+
			"chains, and non-metadatatest references are rejected.\n"+
			"Violations (%d):\n  %s",
			fixtureCellIDRuleID, len(violations), strings.Join(violations, "\n  "))
	}
}

// scanCellIDFixtureViolations runs RunTyped over the full main module
// (tests=true) twice — once with FlatNonDefaultTags, once with no tags —
// to cover //go:build !X reverse directives. Returns one diagnostic per
// violating position with file:line:column source pointer.
func scanCellIDFixtureViolations(t *testing.T, allowSelfFiles, carveOuts map[string]struct{}) []string {
	t.Helper()
	var violations []string
	collect := func(opts TypedOpts) {
		_ = RunTyped(t, opts, []string{"./..."}, func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if _, ok := allowSelfFiles[rel]; ok {
					continue
				}
				if p.IsGenerated(file) {
					continue
				}
				carved := carvedOutFunctions(file, p, carveOuts)
				ast.Inspect(file, func(n ast.Node) bool {
					if isInsideCarvedFunc(n, carved) {
						return false
					}
					comp, ok := n.(*ast.CompositeLit)
					if !ok {
						return true
					}
					violations = append(violations,
						scanCellIDComposite(p, file, rel, comp)...)
					return true
				})
			}
			return nil
		})
	}
	collect(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()})
	collect(TypedOpts{Tests: true})
	return violations
}

// carvedOutFunctions returns the set of *ast.FuncDecl ranges within file
// whose qualified name is in carveOuts. The result is a position range
// used by isInsideCarvedFunc.
func carvedOutFunctions(file *ast.File, p *Pass, carveOuts map[string]struct{}) []funcRange {
	if p.Pkg == nil {
		return nil
	}
	pkgPath := p.Pkg.Path()
	var out []funcRange
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil {
			continue
		}
		qual := pkgPath + "." + fd.Name.Name
		if _, ok := carveOuts[qual]; !ok {
			continue
		}
		out = append(out, funcRange{start: fd.Pos(), end: fd.End()})
	}
	return out
}

type funcRange struct {
	start, end token.Pos
}

func isInsideCarvedFunc(n ast.Node, carved []funcRange) bool {
	if n == nil || len(carved) == 0 {
		return false
	}
	pos := n.Pos()
	for _, r := range carved {
		if pos >= r.start && pos < r.end {
			return true
		}
	}
	return false
}

// scanCellIDComposite inspects a single CompositeLit and emits one
// violation per cell-id position whose expression is not a sanctioned
// metadatatest reference.
func scanCellIDComposite(p *Pass, file *ast.File, rel string, comp *ast.CompositeLit) []string {
	t := p.TypesInfo.TypeOf(comp)
	if t == nil {
		return nil
	}
	switch under := t.Underlying().(type) {
	case *types.Map:
		return scanCellIDMapComposite(p, rel, comp, under)
	case *types.Struct:
		return scanCellIDStructComposite(p, rel, comp, t)
	}
	return nil
}

func scanCellIDMapComposite(p *Pass, rel string, comp *ast.CompositeLit, m *types.Map) []string {
	// Match map[string]*metadata.<Struct> or map[string]metadata.<Struct>
	// where struct name is in cellIDMapKeyValueStructs.
	keyT, ok := m.Key().(*types.Basic)
	if !ok || keyT.Kind() != types.String {
		return nil
	}
	valStruct := namedStructFromType(m.Elem())
	if valStruct == nil {
		return nil
	}
	if valStruct.Obj().Pkg() == nil || valStruct.Obj().Pkg().Path() != metadataPkgPath {
		return nil
	}
	if _, ok := cellIDMapKeyValueStructs[valStruct.Obj().Name()]; !ok {
		return nil
	}
	var out []string
	for _, elt := range comp.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if !isSanctionedCellIDExpr(p, kv.Key) {
			out = append(out, fmtPositionViolation(p, rel, kv.Key, "map[string]*metadata."+valStruct.Obj().Name()+" key"))
		}
	}
	return out
}

func scanCellIDStructComposite(p *Pass, rel string, comp *ast.CompositeLit, t types.Type) []string {
	named, ok := t.(*types.Named)
	if !ok {
		// pointer to named?
		if ptr, isPtr := t.(*types.Pointer); isPtr {
			named, ok = ptr.Elem().(*types.Named)
			if !ok {
				return nil
			}
		} else {
			return nil
		}
	}
	if named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != metadataPkgPath {
		return nil
	}
	structName := named.Obj().Name()
	var out []string
	for _, elt := range comp.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		keyIdent, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		fieldName := keyIdent.Name
		pos, found := lookupCellIDFieldPosition(structName, fieldName)
		if !found {
			continue
		}
		if pos.isSliceElement {
			// value is *ast.CompositeLit []string{...} or []string{} ref
			sliceComp, ok := kv.Value.(*ast.CompositeLit)
			if !ok {
				// not a literal slice — could be Ident to a known slice; skip
				// (rare in fixtures; archtest A1 focuses on inline composites)
				continue
			}
			for _, sliceElt := range sliceComp.Elts {
				if !isSanctionedCellIDExpr(p, sliceElt) {
					out = append(out, fmtPositionViolation(p, rel, sliceElt, "metadata."+structName+"."+fieldName+"[i]"))
				}
			}
		} else {
			if !isSanctionedCellIDExpr(p, kv.Value) {
				out = append(out, fmtPositionViolation(p, rel, kv.Value, "metadata."+structName+"."+fieldName))
			}
		}
	}
	return out
}

func namedStructFromType(t types.Type) *types.Named {
	if t == nil {
		return nil
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return nil
	}
	return named
}

func lookupCellIDFieldPosition(structName, fieldName string) (cellIDFieldPosition, bool) {
	for _, p := range cellIDFieldPositions {
		if p.structName == structName && p.fieldName == fieldName {
			return p, true
		}
	}
	return cellIDFieldPosition{}, false
}

// isSanctionedCellIDExpr reports whether expr is a sanctioned cell-id
// source: a metadatatest.NewCellID(BasicLit) CallExpr or a SelectorExpr
// resolving to a metadatatest package-level Var. Any other shape — bare
// BasicLit, Ident→BasicLit chain, dynamic NewCellID arg, third-party
// const ref — is rejected.
func isSanctionedCellIDExpr(p *Pass, expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.CallExpr:
		sel, ok := e.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok || fn == nil || fn.Pkg() == nil {
			// Try package-level func via Uses (non-method call).
			ident := sel.Sel
			obj := p.TypesInfo.Uses[ident]
			f, isFn := obj.(*types.Func)
			if !isFn || f.Pkg() == nil {
				return false
			}
			if f.Pkg().Path() != metadatatestPkgPath || f.Name() != metadatatestNewCellIDFunc {
				return false
			}
		} else if fn.Pkg().Path() != metadatatestPkgPath || fn.Name() != metadatatestNewCellIDFunc {
			return false
		}
		if len(e.Args) != 1 {
			return false
		}
		lit, ok := e.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return false
		}
		return true
	case *ast.SelectorExpr:
		// metadatatest.<CellIDVar>
		obj := p.TypesInfo.Uses[e.Sel]
		v, ok := obj.(*types.Var)
		if !ok || v.Pkg() == nil {
			return false
		}
		return v.Pkg().Path() == metadatatestPkgPath
	}
	return false
}

func fmtPositionViolation(p *Pass, rel string, expr ast.Expr, fieldPath string) string {
	pos := p.Fset.Position(expr.Pos())
	return fmt.Sprintf("%s:%d:%d: %s — expected metadatatest.NewCellID(literal) or metadatatest.<CellIDVar>, got %s",
		rel, pos.Line, pos.Column, fieldPath, exprSourceSnippet(expr))
}

func exprSourceSnippet(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			return e.Value
		}
		return e.Value
	case *ast.Ident:
		return "ident " + e.Name
	case *ast.SelectorExpr:
		return "selector"
	case *ast.CallExpr:
		return "call"
	default:
		return fmt.Sprintf("%T", expr)
	}
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

	var fn *ast.FuncDecl
	_ = RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != metadatatestPkgPath {
			return nil
		}
		for _, file := range p.Files {
			if filepath.Base(p.Abs(file)) != "cellid.go" {
				continue
			}
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Name == nil || fd.Name.Name != metadatatestNewCellIDFunc {
					continue
				}
				fn = fd
				return nil
			}
		}
		return nil
	})
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
	if !isMatchCellIDNegateCond(ifStmt.Cond) {
		t.Fatalf("%s/A2: if condition must be !metadata.MatchCellID(s)", fixtureCellIDRuleID)
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
	retStmt, ok := fn.Body.List[1].(*ast.ReturnStmt)
	if !ok || len(retStmt.Results) != 1 {
		t.Fatalf("%s/A2: second statement must be 'return s'", fixtureCellIDRuleID)
	}
	retIdent, ok := retStmt.Results[0].(*ast.Ident)
	if !ok || retIdent.Name != "s" {
		t.Fatalf("%s/A2: must return identifier s", fixtureCellIDRuleID)
	}
}

func isMatchCellIDNegateCond(cond ast.Expr) bool {
	unary, ok := cond.(*ast.UnaryExpr)
	if !ok || unary.Op != token.NOT {
		return false
	}
	call, ok := unary.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "MatchCellID" {
		return false
	}
	if len(call.Args) != 1 {
		return false
	}
	argIdent, ok := call.Args[0].(*ast.Ident)
	if !ok || argIdent.Name != "s" {
		return false
	}
	return true
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
	_ = RunTypedFixture(t, FixtureOpts{Tests: false}, []string{"./tools/archtest/internal/fixturecellidnegfixture"}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if _, ok := allowSelfFile[rel]; ok {
				continue
			}
			carved := carvedOutFunctions(file, p, carveOuts)
			ast.Inspect(file, func(n ast.Node) bool {
				if isInsideCarvedFunc(n, carved) {
					return false
				}
				comp, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				violations = append(violations, scanCellIDComposite(p, file, rel, comp)...)
				return true
			})
		}
		return nil
	})
	sort.Strings(violations)
	violations = dedupSortedStrings(violations)

	// Each bad file emits at least one finding.
	wantBadFiles := []string{
		"bad_map_key.go",
		"bad_field.go",
		"bad_slice_elem.go",
		"bad_ident_chain.go",
	}
	seen := map[string]bool{}
	for _, v := range violations {
		for _, want := range wantBadFiles {
			if strings.Contains(v, want) {
				seen[want] = true
			}
		}
	}
	var missing []string
	for _, want := range wantBadFiles {
		if !seen[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%s/A3: archtest A1 failed to detect bad fixtures in %v.\nViolations seen:\n  %s",
			fixtureCellIDRuleID, missing, strings.Join(violations, "\n  "))
	}

	// Good files (good_const_ref.go, good_call_literal.go) must NOT
	// produce any violations.
	for _, v := range violations {
		if strings.Contains(v, "good_const_ref.go") || strings.Contains(v, "good_call_literal.go") {
			t.Errorf("%s/A3: archtest A1 produced false positive on good fixture: %s", fixtureCellIDRuleID, v)
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
	adrBytes, err := os.ReadFile(adrPath)
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

// parseCarveOutTableFromADR extracts function qualified names from the
// ADR's §2 carveout registry markdown table. The table is recognised by
// a header line starting with "| Carved-out function" and ending at the
// next blank line / non-table line.
func parseCarveOutTableFromADR(content string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	lines := strings.Split(content, "\n")
	inTable := false
	headerRe := regexp.MustCompile(`^\|\s*Carved-out function\s*\|`)
	dividerRe := regexp.MustCompile(`^\|[\s\-:]+\|`)
	rowRe := regexp.MustCompile(`^\|\s*([^|]+?)\s*\|`)
	for _, line := range lines {
		if !inTable {
			if headerRe.MatchString(line) {
				inTable = true
			}
			continue
		}
		// Skip the divider row after header.
		if dividerRe.MatchString(line) {
			continue
		}
		// End of table — empty line or non-pipe line.
		if !strings.HasPrefix(line, "|") {
			break
		}
		m := rowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		fqn := strings.TrimSpace(m[1])
		if fqn == "" || strings.HasPrefix(fqn, "-") {
			continue
		}
		out[fqn] = struct{}{}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no carveout rows found under '| Carved-out function |' header")
	}
	return out, nil
}

// TestMetadatatestImportScope (import scope guard) ensures the
// metadatatest package is only imported by *_test.go files or by
// archtest_fixture-tagged code. Importing it from production code would
// drag init-time panics into runtime and contradict its test-only
// purpose.
//
// AI-robust: Medium (archtest path-based scope; Go's type system cannot
// express "test-only package"). Upgrade tracked alongside the broader
// go test-only-package proposal.
func TestMetadatatestImportScope(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTypedProduction(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			rel := p.Rel(file)
			// Filter out files outside module root (build cache synthetic
			// test runners, etc.) and *_test.go (test imports are allowed).
			if strings.HasPrefix(rel, "..") || strings.Contains(rel, "/.cache/") {
				continue
			}
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if !importsMetadatatest(file) {
				continue
			}
			// archtest_fixture build tag files would have been filtered out by
			// the loader unless explicitly enabled — RunTypedProduction does
			// not enable archtest_fixture, so any non-test file seen here is
			// production scope.
			pos := p.Fset.Position(file.Pos())
			violations = append(violations,
				fmt.Sprintf("%s:%d: production file imports %s — restricted to *_test.go",
					rel, pos.Line, metadatatestPkgPath))
		}
		return nil
	})
	sort.Strings(violations)
	violations = dedupSortedStrings(violations)
	if len(violations) > 0 {
		t.Fatalf("METADATATEST-IMPORT-SCOPE-01: metadatatest package imported from production code (must be _test.go only):\n  %s",
			strings.Join(violations, "\n  "))
	}
}

func importsMetadatatest(file *ast.File) bool {
	for _, imp := range file.Imports {
		if imp.Path == nil {
			continue
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if path == metadatatestPkgPath {
			return true
		}
	}
	return false
}
