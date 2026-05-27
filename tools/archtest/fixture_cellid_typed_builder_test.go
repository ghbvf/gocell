// invariants asserted in this file:
//   - INVARIANT: FIXTURE-CELLID-TYPED-BUILDER-01
//   - INVARIANT: METADATATEST-IMPORT-SCOPE-01
//
// FIXTURE-CELLID-TYPED-BUILDER-01: every cell-id field position in a
// kernel/metadata.* struct or map composite literal — anywhere in the
// module's hand-written code (production + tests, all tag combinations) —
// must be sourced from kernel/metadata/metadatatest.NewCellID(literal)
// or one of metadatatest's pre-validated package-level cell-id vars
// (CellID*). Bare string literals at those positions are rejected.
//
// Scope: currently kernel/ only. Test fixtures in runtime/, cells/, cmd/,
// examples/, and tools/ outside the migrated tools/codegen +
// tools/generatedverify subset may still embed bare cell-id literals (Soft
// state). Mirror backlog issue #1201 tracks scope expansion to non-kernel/
// packages.
//
// METADATATEST-IMPORT-SCOPE-01: the metadatatest package
// (kernel/metadata/metadatatest) must only be imported by *_test.go
// files or archtest_fixture-tagged code. Importing it from production
// code drags init-time panics into runtime and contradicts its
// test-only purpose. AI-robust: Medium (archtest path-based scope; Go
// cannot express "test-only package" at the type level). Upgrade
// tracked alongside the broader go test-only-package proposal.
//
// Enforcement is split into four sub-tests (A1–A4) plus the import
// scope guard:
//
//   - A1 TestFixtureCellIDTypedBuilder — typed-info funnel: scans all
//     *ast.CompositeLit, resolves each to a kernel/metadata.* struct or
//     map type, identifies the cell-id field positions (15-field
//     enumeration below: 14 struct fields + 1 map key), and asserts
//     every expression at such a position resolves to
//     metadatatest.NewCellID(BasicLit) or metadatatest.<CellIDVar>. Hard
//     downstream: callsite identity is verified via go/types — Ident→
//     BasicLit chains, third-party consts, dynamic NewCellID arguments,
//     and non-CellID-prefixed metadatatest vars are rejected uniformly.
//
//     Known blind spots (A1 scope — documented per ai-robust.md §载体决策原则
//     "强制盲区自检"):
//
//   - Ident-typed slice values: when a slice field (e.g.
//     JourneyMeta.Cells) is assigned via an *ast.Ident pointing to a
//     pre-built []string var rather than an inline []string{...}
//     composite literal, the outer kv.Value is not a *ast.CompositeLit
//     and scanCellIDStructComposite silently skips the check. A1 only
//     enforces inline composite literals. Reverse self-test:
//     blind_spot_ident_slice.go asserts A1 does not report a
//     violation for this shape (documents the behavior, does not close
//     the gap).
//
//   - Assignment statement form (c.ID = id): A1 scans CompositeLit
//     nodes only; `var c = &metadata.CellMeta{}; c.ID = "rawassign"`
//     is outside A1 scope. This form appears in makeProject helpers
//     in kernel/metadata/derived_test.go and assembly_derive_test.go.
//     Reverse self-test: blind_spot_assign.go asserts A1 does not
//     report a violation for this shape.
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
//     element, Ident→BasicLit chain, additional struct/slice positions)
//     plus pass-through good cases and blind-spot self-test files, and
//     asserts A1 fires exactly on the bad cases. Reverse self-test
//     that guards against A1 over-broad or no-op regressions.
//
//   - A4 TestFixtureCellIDTypedBuilder_CarveOutADRConsistency — parses
//     the fixtureCellIDCarveOuts map in this file alongside the carveout
//     registry table in docs/architecture/<ts>-adr-fixture-cellid-typed-
//     builder.md, asserting both sides are character-identical. Mirrors
//     ERRCODE-CARVEOUT-ADR-CONSISTENCY-01.
//
//   - TestMetadatatestImportScope — enforces METADATATEST-IMPORT-SCOPE-01:
//     no production (.go non-_test.go) file may import metadatatest.
//     Runs two RunTypedProduction passes (with FlatNonDefaultTags and
//     without) to cover //go:build !X reverse directives.
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
// Hard (A2 body form-uniqueness + pkg-path identity lock), meta Hard (A3
// negative fixture + A4 ADR consistency). The 15-field enumeration (14
// struct fields + 1 map key) is a closed schema-derived set; new cell-id
// fields require a same-PR update to both this file and the ADR §1 field
// table. See ADR §3 升级路径. A1 is not in the PR-time governance.yml
// 4-class core invariant set; it is covered by nightly
// archtest-nightly.yml 16-shard matrix — this is an intentional latency
// tradeoff (see ADR §4).
//
// ref: tools/archtest/cell_id_pattern_single_source_test.go — sibling
//
//	typeseval funnel (Medium; PR #484).
//
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
	// fixtureCellIDADRFile is the relative path to the ADR backing A4
	// (carveout map ↔ ADR registry consistency). Rename or move of the ADR
	// file requires synchronized update of this constant in the SAME PR —
	// A4 fails fast with "read ADR" if the file is not found, which is the
	// intended diagnostic.
	fixtureCellIDADRFile = "docs/architecture/202605271000-adr-fixture-cellid-typed-builder.md"
)

// cellIDFieldPosition identifies a struct field (or slice-field element
// position) whose string value semantics is a cell-id and therefore must
// be sourced from metadatatest. The 15-field enumeration (14 struct fields
// + 1 map key via cellIDMapKeyValueStructs) mirrors the in-scope table in
// plan #681 / ADR §1; new cell-id fields require a same-PR update here
// AND in the ADR §1 table.
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
	// CellWireSummary.CellID: the derived wire-catalog struct in derived.go whose
	// CellID field carries cell-id semantics. Fixtures constructing CellWireSummary
	// live in runtime/ (outside A1's current kernel/ scope) and will be enforced
	// once #1201 expands scope; this entry is forward-compatible.
	{"CellWireSummary", "CellID", false},
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
		// derived.go is the single production construction site for CellWireSummary.
		// cellWireSummaryFrom sets CellID from a function parameter (not a literal),
		// which is semantically correct — A1 does not gate production assignment from
		// runtime values, only fixture literal embedding. CellWireSummary.CellID is in
		// cellIDFieldPositions for forward-compatible enforcement in runtime/ test
		// fixtures (tracked by #1201), not to gate this production constructor.
		"kernel/metadata/derived.go": {},
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

// scanCellIDFixtureViolations runs RunTyped over the kernel/ package
// tree (tests=true) twice — once with FlatNonDefaultTags, once with no
// tags — to cover //go:build !X reverse directives. Returns one
// diagnostic per violating position with file:line:column source
// pointer.
//
// Scope rationale: this rule's scope tracks the plan and ADR §1 scope
// — kernel/ is where the cell-id metadata fixtures originate and the
// in-scope cell-id field positions live. Non-kernel packages (runtime/,
// cells/, cmd/, examples/, tools/) consume these structs in their own
// fixtures and will be migrated via mirror backlog issues; once each
// such package is migrated, its path prefix is added to the scan scope
// list below and its allowlist entry (if any) removed.
func scanCellIDFixtureViolations(t *testing.T, allowSelfFiles, carveOuts map[string]struct{}) []string {
	t.Helper()
	scopePrefixes := []string{"kernel/"}
	var violations []string
	collect := func(opts TypedOpts) {
		_ = RunTyped(t, opts, []string{"./kernel/..."}, func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if !hasAnyPrefix(rel, scopePrefixes) {
					continue
				}
				if _, ok := allowSelfFiles[rel]; ok {
					continue
				}
				if p.IsGenerated(file) {
					continue
				}
				carved := carvedOutFunctions(file, p, carveOuts)
				EachInSubtree[ast.CompositeLit](file, func(comp *ast.CompositeLit) {
					if isInsideCarvedFunc(comp, carved) {
						return
					}
					violations = append(violations,
						scanCellIDComposite(p, file, rel, comp)...)
				})
			}
			return nil
		})
	}
	collect(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()})
	collect(TypedOpts{Tests: true})
	return violations
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
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
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil {
			return
		}
		qual := pkgPath + "." + fd.Name.Name
		if _, ok := carveOuts[qual]; !ok {
			return
		}
		out = append(out, funcRange{start: fd.Pos(), end: fd.End()})
	})
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
func scanCellIDComposite(p *Pass, _ *ast.File, rel string, comp *ast.CompositeLit) []string {
	t := p.TypesInfo.TypeOf(comp)
	if t == nil {
		return nil
	}
	switch under := t.Underlying().(type) {
	case *types.Map:
		return scanCellIDMapComposite(p, rel, comp, under)
	case *types.Struct:
		return scanCellIDStructComposite(p, rel, comp, t)
	case *types.Pointer:
		// Pointer-elided composite literal: []*metadata.ContractMeta{{OwnerCell: "bare"}}
		// has the inner literal typed as *metadata.ContractMeta. t.Underlying() is *types.Pointer
		// (a pointer is its own underlying type). Unwrap to the element type and proceed as
		// if the literal were a struct composite.
		if _, ok := under.Elem().Underlying().(*types.Struct); ok {
			return scanCellIDStructComposite(p, rel, comp, under.Elem())
		}
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
	valName := valStruct.Obj().Name()
	EachInChildren[ast.KeyValueExpr](comp, func(kv *ast.KeyValueExpr) {
		if !isSanctionedCellIDExpr(p, kv.Key) {
			out = append(out, fmtPositionViolation(p, rel, kv.Key, "map[string]*metadata."+valName+" key"))
		}
	})
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
	EachInChildren[ast.KeyValueExpr](comp, func(kv *ast.KeyValueExpr) {
		keyIdent, ok := kv.Key.(*ast.Ident)
		if !ok {
			return
		}
		fieldName := keyIdent.Name
		pos, found := lookupCellIDFieldPosition(structName, fieldName)
		if !found {
			return
		}
		if pos.isSliceElement {
			// value is *ast.CompositeLit []string{...} or []string{} ref
			sliceComp, ok := kv.Value.(*ast.CompositeLit)
			if !ok {
				// not a literal slice — could be Ident to a known slice; skip
				// (rare in fixtures; archtest A1 focuses on inline composites)
				return
			}
			for _, sliceElt := range sliceComp.Elts {
				if !isSanctionedCellIDExpr(p, sliceElt) {
					out = append(out, fmtPositionViolation(p, rel, sliceElt, "metadata."+structName+"."+fieldName+"[i]"))
				}
			}
		} else if !isSanctionedCellIDExpr(p, kv.Value) {
			out = append(out, fmtPositionViolation(p, rel, kv.Value, "metadata."+structName+"."+fieldName))
		}
	})
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
			// Reject methods (receiver != nil): a method named NewCellID on some
			// other type could share the same pkg+name and slip through if we only
			// checked package path and function name.
			if f.Signature().Recv() != nil {
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
		// metadatatest.<CellIDVar> — the var name must have a "CellID" prefix so that
		// future non-CellID vars added to the metadatatest package are not silently
		// accepted as sanctioned cell-id sources.
		obj := p.TypesInfo.Uses[e.Sel]
		v, ok := obj.(*types.Var)
		if !ok || v.Pkg() == nil {
			return false
		}
		if v.Pkg().Path() != metadatatestPkgPath {
			return false
		}
		return strings.HasPrefix(v.Name(), "CellID")
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
		return e.Value
	case *ast.Ident:
		return "ident " + e.Name
	case *ast.SelectorExpr:
		// Return <pkg>.<Name> to give actionable context in violation messages.
		if xIdent, ok := e.X.(*ast.Ident); ok {
			return xIdent.Name + "." + e.Sel.Name
		}
		return "selector." + e.Sel.Name
	case *ast.CallExpr:
		// Return <funcName>() for actionable context.
		switch fn := e.Fun.(type) {
		case *ast.Ident:
			return fn.Name + "()"
		case *ast.SelectorExpr:
			if xIdent, ok := fn.X.(*ast.Ident); ok {
				return xIdent.Name + "." + fn.Sel.Name + "()"
			}
			return fn.Sel.Name + "()"
		}
		return "call()"
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

	// A2 loads only the metadatatest package — no need to pull in the entire
	// module type-graph. RunTyped with a single-package pattern is faster and
	// matches the "narrow scope" guidance in ai-robust.md §载体决策原则.
	type a2Result struct {
		fn    *ast.FuncDecl
		pInfo *types.Info
	}
	var result a2Result
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./kernel/metadata/metadatatest/..."}, func(p *Pass) []Diagnostic {
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
	// F1: verify Approved resolves to panicregister.Approved via TypesInfo — a
	// SelectorExpr with .Name=="Approved" is not sufficient; a homonymous function
	// in another package would silently slip through. Package-path lock is Hard.
	if pInfo != nil {
		approvedObj := pInfo.Uses[approvedSel.Sel]
		approvedFn, isFn := approvedObj.(*types.Func)
		const panicregPkgPath = "github.com/ghbvf/gocell/pkg/panicregister"
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
	if pInfo != nil {
		assertObj := pInfo.Uses[assertSel.Sel]
		assertFn, isFn := assertObj.(*types.Func)
		const errcodePkgPath = "github.com/ghbvf/gocell/pkg/errcode"
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
	visitedFiles := make(map[string]struct{})
	fixturePkgPattern := []string{"./tools/archtest/internal/fixturecellidnegfixture"}
	_ = RunTypedFixture(t, FixtureOpts{Tests: false}, fixturePkgPattern, func(p *Pass) []Diagnostic {
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
	// loaded by RunTypedFixture. If a file is absent (e.g. build-tag mismatch
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
			t.Errorf("%s/A3: expected fixture file %s was not loaded by RunTypedFixture (build-tag or path issue?)", fixtureCellIDRuleID, want)
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

// parseCarveOutTableFromADR extracts function qualified names from the
// ADR's §2 carveout registry markdown table. The table is recognized by
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
// Two RunTypedProduction passes are performed — one with
// FlatNonDefaultTags and one without — to cover //go:build !X reverse
// build directives (files that are excluded by default tags but included
// with non-default tags, or vice-versa).
//
// AI-robust: Medium (archtest path-based scope; Go's type system cannot
// express "test-only package"). Upgrade tracked alongside the broader
// go test-only-package proposal.
func TestMetadatatestImportScope(t *testing.T) {
	t.Parallel()

	var violations []string
	collectImportViolations := func(opts TypedOpts) {
		_ = RunTypedProduction(t, opts, func(p *Pass) []Diagnostic {
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
	}
	// First pass: with FlatNonDefaultTags to cover //go:build !X forms.
	collectImportViolations(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()})
	// Second pass: without extra tags (default build context).
	collectImportViolations(TypedOpts{Tests: true})
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
