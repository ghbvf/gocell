package archtest

// INVARIANT: SCANNER-FRAMEWORK-USAGE-01
//
// scanner_framework_usage_test.go — guard archtest tools/archtest/*_test.go
// from bypassing the shared scanner framework at tools/archtest/internal/scanner.
//
// Single-rule file per CLAUDE.md "新增 invariant 决策原则" file naming branch
// (single rule → {rule}_test.go). Promote to {theme}_invariants_test.go if
// related SCANNER-* invariants accumulate to ≥ 3.

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// INVARIANT: SCANNER-FRAMEWORK-USAGE-01
//
// archtest *_test.go files at tools/archtest/<file>_test.go must use the
// tools/archtest/internal/scanner framework. Forbidden, on three paths:
//
//	Path A — package-level symbol calls and dot-imports of these import paths:
//	  path/filepath: WalkDir, Walk, Glob
//	  os:            ReadDir
//	  io/ioutil:     ReadDir   (deprecated but still callable)
//	  io/fs:         WalkDir, Walk, Glob, ReadDir
//	  go/ast:                              Inspect, Walk, Preorder
//	  golang.org/x/tools/go/ast/inspector: New, Preorder, Nodes, WithStack, All, PreorderSeq
//
//	Path A' — method calls whose receiver type is a named type from a banned
//	  package, e.g. `f := os.Open(dir); f.ReadDir(-1)` resolves to
//	  (*os.File).ReadDir; `var fsys fs.ReadDirFS; fsys.ReadDir(".")` resolves
//	  to (fs.ReadDirFS).ReadDir. Type-aware via go/types Info — closes
//	  PR430-FU-USAGE-01-TYPE-AWARE backlog (PackageAliases-based AST scan
//	  missed these forms).
//
//	Path B — `for _, X := range Y { X.(*ast.W) }` when Y's static type is
//	  []ast.{Decl,Spec,Stmt,Expr}. Equivalent to bare ast.Inspect: the type
//	  assertion in the loop body dispatches by node kind at runtime; AI may
//	  write/omit the wrong assertion silently. Use
//	  scanner.EachInSubtree[ast.W](root, func(*ast.W){...}) instead.
//
// All three paths share a single typeseval.SharedResolver entry: one
// packages.Load for the entire archtest tree, reused across paths via the
// process-wide singleflight cache (also used by rmq_invariants and 19 other
// type-aware archtests).
//
// Use scanner.DirsScope/ModuleScope + EachFile (.go), EachContentFile
// (YAML/JSON/MD/SQL/...), MatchRels (glob-style filter), IncludeTestdata /
// IncludeGenerated (default-skipped dirs), and scanner.EachInSubtree[N] /
// scanner.EachInChildren[N] for typed AST iteration.
//
// Coverage:
//   - SelectorExpr scan via scanner.EachInSubtree[ast.SelectorExpr] (dogfood —
//     the framework's first consumer is the rule that enforces it).
//   - Path A: dot-import scan flags `import . "<pkg>"` directly; SelectorExpr
//     scan resolves package-level calls via info.Uses[id].(*types.PkgName).
//   - Path A': SelectorExpr scan resolves receiver types via info.Types[X];
//     covers pointer types (*os.File) and interface types (fs.FS / fs.ReadDirFS).
//   - Path B: scanner.EachInSubtree[ast.RangeStmt] then
//     scanner.EachInSubtree[ast.TypeAssertExpr] with binding-name + ast-list
//     element-kind verification.
//
// Cannot funnel: the rule itself enforces consumer use of the funnel; the
// type system cannot tell apart "framework-internal walk" (legitimate) from
// "consumer raw walk" (forbidden). framework-internal scanner.EachInSubtree is
// in tools/archtest/internal/scanner, which is not in this scan's scope.
//
// New rules MUST go through the scanner framework + EachInSubtree/EachInChildren.
func TestScannerFrameworkUsage01(t *testing.T) {
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, true, nil, "./tools/archtest/...")
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}

	var diags []scanner.Diagnostic
	for _, pkg := range resolver.Packages() {
		if pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			// Only flag top-level archtest test files (tools/archtest/<file>_test.go).
			// Subpackages under tools/archtest/internal/ are out of scope.
			if filepath.ToSlash(filepath.Dir(rel)) != "tools/archtest" {
				continue
			}
			if !strings.HasSuffix(rel, "_test.go") {
				continue
			}
			diags = append(diags, forbiddenWalkRefs(pkg.TypesInfo, pkg.Fset, file, rel)...)
			diags = append(diags, forbiddenAstListTypeAssertions(pkg.TypesInfo, pkg.Fset, file, rel)...)
		}
	}
	scanner.Report(t, "SCANNER-FRAMEWORK-USAGE-01", diags)
}

// forbiddenWalkImports lists the import paths whose directory-traversal /
// AST-walk symbols are banned in archtest tests. Order is fixed so
// diagnostics are emitted deterministically.
var forbiddenWalkImports = []string{
	"path/filepath",
	"os",
	"io/ioutil",
	"io/fs",
	"go/ast",
	"golang.org/x/tools/go/ast/inspector",
}

// forbiddenWalkSymbols maps each banned import path to the package-level
// symbols (functions, vars) that must not be referenced in archtest tests.
//
// Adding a new primitive (e.g. "embed.FS.ReadDir" via fs.ReadDirFS) means
// extending this table. Method calls on receivers of named types from these
// import paths are caught separately by forbiddenMethodSymbols (path A').
var forbiddenWalkSymbols = map[string][]string{
	"path/filepath":                       {"WalkDir", "Walk", "Glob"},
	"os":                                  {"ReadDir"},
	"io/ioutil":                           {"ReadDir"},
	"io/fs":                               {"WalkDir", "Walk", "Glob", "ReadDir"},
	"go/ast":                              {"Inspect", "Walk", "Preorder"},
	"golang.org/x/tools/go/ast/inspector": {"New", "Preorder", "Nodes", "WithStack", "All", "PreorderSeq"},
}

// forbiddenMethodSymbols maps banned receiver-type import paths to the
// method names that must not be invoked on values of named types from those
// packages. Resolved via go/types Info — covers
//
//	(*os.File).ReadDir
//	(fs.FS).ReadDir / (fs.ReadDirFS).ReadDir / (fs.GlobFS).Glob / WalkDir variants
//
// Coverage limit: embed.FS is intentionally NOT listed here. Although
// embed.FS exposes a ReadDir method, archtest never reads embedded data
// at-rest (it scans live source code via go/parser); embed.FS misuse is
// a runtime-data concern, not a scanner-bypass route. If an archtest ever
// needs to ban embed.FS.ReadDir, add `"embed": {"ReadDir"}` here and a
// fixture case.
//
// Closes backlog PR430-FU-USAGE-01-TYPE-AWARE — the prior PackageAliases-
// based AST scan could not see these because it had no type info.
var forbiddenMethodSymbols = map[string][]string{
	"os":    {"ReadDir"},
	"io/fs": {"ReadDir", "WalkDir", "Glob"},
}

// forbiddenWalkRefs reports any reference to a banned package-level symbol or
// type-method, with two branches: dot-import (1) and SelectorExpr scan (2).
// Type-aware via *types.Info: package-level calls resolved via
// info.Uses[id].(*types.PkgName); receiver-type method calls resolved via
// info.Types[sel.X].
//
// Signature: minimal type-info dependency `(*types.Info, *token.FileSet,
// *ast.File, rel)`. Production callers pass (pkg.TypesInfo, pkg.Fset, file,
// pkgFileRel(...)); fixture callers pass (minimalCheck.Info, fset, file,
// "fake.go"). Same pure function for both — fixture/prod cannot drift.
//
// SelectorExpr iteration uses scanner.EachInSubtree (dogfood — the rule that
// enforces the framework is itself implemented in the framework).
func forbiddenWalkRefs(info *types.Info, fset *token.FileSet, file *ast.File, rel string) []scanner.Diagnostic {
	var out []scanner.Diagnostic

	// (1) Dot-import branch.
	for _, imp := range file.Imports {
		if imp == nil || imp.Path == nil || imp.Name == nil || imp.Name.Name != "." {
			continue
		}
		for _, banned := range forbiddenWalkImports {
			if imp.Path.Value == strconv.Quote(banned) {
				out = append(out, scanner.Diagnostic{
					Rel:     rel,
					Line:    fset.Position(imp.Pos()).Line,
					Message: fmt.Sprintf("dot-import of %q forbidden in archtest tests; use named import + tools/archtest/internal/scanner", banned),
				})
			}
		}
	}

	// (2) Type-aware SelectorExpr scan.
	scanner.EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		// (2a) Package-level call: sel.X is *ast.Ident bound to imported pkg.
		if id, ok := sel.X.(*ast.Ident); ok {
			if pkgName, isPkg := info.Uses[id].(*types.PkgName); isPkg {
				importPath := pkgName.Imported().Path()
				if symbols, banned := forbiddenWalkSymbols[importPath]; banned && contains(symbols, sel.Sel.Name) {
					out = append(out, scanner.Diagnostic{
						Rel:     rel,
						Line:    fset.Position(sel.Pos()).Line,
						Message: fmt.Sprintf("use tools/archtest/internal/scanner instead of %s.%s", importPath, sel.Sel.Name),
					})
					return
				}
			}
		}
		// (2b) Method call on receiver type: (*os.File).ReadDir, (fs.FS).WalkDir, etc.
		if tv, ok := info.Types[sel.X]; ok && tv.Type != nil {
			if recvImportPath := namedTypeImportPath(tv.Type); recvImportPath != "" {
				if methods, banned := forbiddenMethodSymbols[recvImportPath]; banned && contains(methods, sel.Sel.Name) {
					out = append(out, scanner.Diagnostic{
						Rel:     rel,
						Line:    fset.Position(sel.Pos()).Line,
						Message: fmt.Sprintf("use tools/archtest/internal/scanner instead of (%s).%s", recvImportPath, sel.Sel.Name),
					})
				}
			}
		}
	})
	return out
}

// forbiddenAstListTypeAssertions reports for-range over []ast.{Decl,Spec,Stmt,Expr}
// with type assertions/switches in the body. Equivalent to bare ast.Inspect:
// the type assertion in the loop body dispatches by node kind at runtime; AI
// may write/omit the wrong assertion silently. Use
// scanner.EachInChildren[ast.W](root, func(*ast.W){...}) for direct-child
// semantics, or scanner.EachInSubtree[ast.W] for full-subtree, instead.
//
// Detection precision:
//   - Outer: scanner.EachInSubtree[ast.RangeStmt] over file
//   - rs.X must have static type []ast.{Decl|Spec|Stmt|Expr} via info.Types
//   - Inner forms (all flagged):
//     a) `for _, X := range Y { X.(*ast.W) }` — rs.Value is non-nil (binding
//     name); TypeAssertExpr in body references that binding + *ast.<Kind>
//     b) `for _, X := range Y { switch X := X.(type) { case *ast.W: } }` —
//     TypeSwitchStmt in body with binding-name operand + *ast.<Kind> case
//     c) `for i := range Y { Y[i].(*ast.W) }` — rs.Value is nil (paired-index);
//     TypeAssertExpr in body is IndexExpr(Y, i).(*ast.<Kind>)
//
// Form (c) was previously exempted by the `rs.Value == nil` early-return; that
// exemption is removed now that EachInChildren provides a typed children-only
// API that makes paired-index iteration unnecessary.
func forbiddenAstListTypeAssertions(info *types.Info, fset *token.FileSet, file *ast.File, rel string) []scanner.Diagnostic {
	var diags []scanner.Diagnostic
	scanner.EachInSubtree[ast.RangeStmt](file, func(rs *ast.RangeStmt) {
		if rs.Body == nil {
			return
		}
		elemKind, ok := astListElemKind(info, rs.X)
		if !ok {
			return
		}

		// Form (c): paired-index — `for i := range Y { Y[i].(*ast.W) }`.
		// rs.Value is nil (no value binding), rs.Key is the index variable.
		//
		// Precision guard: if the loop body also uses the same index variable to
		// index a *different* slice (e.g. `Lhs[i]` alongside `Rhs[i]`), the
		// iteration has LHS/RHS pairing semantics and cannot be replaced by
		// EachInChildren. Skip the entire range statement in that case.
		if rs.Value == nil {
			indexName := identNameOf(rs.Key)
			if indexName == "" {
				return
			}
			rsXRepr := exprRepr(rs.X)

			// Check for companion index usage indicating LHS/RHS pairing semantics:
			//   (a) any IndexExpr[i] where X differs from rs.X — e.g. Rhs[i]
			//       alongside Lhs[i] in an assignment.
			//   (b) index variable i passed as a standalone argument to a function
			//       call — e.g. f(OtherSlice, i); the callee typically does
			//       OtherSlice[i] internally, which is also pairing semantics.
			// In both cases EachInChildren cannot replace the paired-index loop.
			companionIndex := false
			scanner.EachInSubtree[ast.IndexExpr](rs.Body, func(idx *ast.IndexExpr) {
				if companionIndex {
					return
				}
				idxId, ok := idx.Index.(*ast.Ident)
				if !ok || idxId.Name != indexName {
					return
				}
				if exprRepr(idx.X) != rsXRepr {
					companionIndex = true
				}
			})
			if !companionIndex {
				// (b) index variable passed as a bare argument to a call.
				// identNameOf is used to avoid a raw TypeAssertExpr inside a
				// for-range over []ast.Expr, which would self-trigger form (a).
				scanner.EachInSubtree[ast.CallExpr](rs.Body, func(call *ast.CallExpr) {
					if companionIndex {
						return
					}
					for _, arg := range call.Args {
						if identNameOf(arg) == indexName {
							companionIndex = true
							return
						}
					}
				})
			}
			if companionIndex {
				return
			}

			scanner.EachInSubtree[ast.TypeAssertExpr](rs.Body, func(ta *ast.TypeAssertExpr) {
				if ta.Type == nil {
					return
				}
				if !isStarAstNodeType(info, ta.Type) {
					return
				}
				idx, ok := ta.X.(*ast.IndexExpr)
				if !ok {
					return
				}
				idxId, ok := idx.Index.(*ast.Ident)
				if !ok || idxId.Name != indexName {
					return
				}
				if exprRepr(idx.X) != rsXRepr {
					return
				}
				diags = append(diags, scanner.Diagnostic{
					Rel:  rel,
					Line: fset.Position(ta.Pos()).Line,
					Message: fmt.Sprintf("use scanner.EachInChildren[ast.X](root, func(*ast.X){...}) "+
						"for direct-child semantics, or scanner.EachInSubtree[ast.X] for full-subtree, "+
						"instead of paired-index for-range over []ast.%s + type assertion", elemKind),
				})
			})
			return
		}

		// Forms (a) and (b): value-binding — `for _, X := range Y { ... }`.
		bindingName := identNameOf(rs.Value)
		if bindingName == "" {
			return
		}
		// Form (a): TypeAssertExpr.
		scanner.EachInSubtree[ast.TypeAssertExpr](rs.Body, func(ta *ast.TypeAssertExpr) {
			if ta.Type == nil {
				return
			}
			if identNameOf(ta.X) != bindingName {
				return
			}
			if !isStarAstNodeType(info, ta.Type) {
				return
			}
			diags = append(diags, scanner.Diagnostic{
				Rel:  rel,
				Line: fset.Position(ta.Pos()).Line,
				Message: fmt.Sprintf("use scanner.EachInChildren[ast.X](root, func(*ast.X){...}) "+
					"for direct-child semantics, or scanner.EachInSubtree[ast.X] for full-subtree, "+
					"instead of for-range over []ast.%s + type assertion", elemKind),
			})
		})
		// Form (b): TypeSwitchStmt — `switch x := X.(type) { case *ast.W: }`.
		scanner.EachInSubtree[ast.TypeSwitchStmt](rs.Body, func(ts *ast.TypeSwitchStmt) {
			if ts.Assign == nil {
				return
			}
			operand := typeSwitchOperand(ts.Assign)
			if operand == nil || identNameOf(operand) != bindingName {
				return
			}
			// At least one case clause must list *ast.<Kind>.
			caseHits := false
			if ts.Body != nil {
				scanner.EachInChildren[ast.CaseClause](ts.Body, func(cc *ast.CaseClause) {
					if caseHits {
						return
					}
					for j := range cc.List {
						if isStarAstNodeType(info, cc.List[j]) {
							caseHits = true
							return
						}
					}
				})
			}
			if !caseHits {
				return
			}
			diags = append(diags, scanner.Diagnostic{
				Rel:  rel,
				Line: fset.Position(ts.Pos()).Line,
				Message: fmt.Sprintf("use scanner.EachInChildren[ast.X](root, func(*ast.X){...}) "+
					"for direct-child semantics, or scanner.EachInSubtree[ast.X] for full-subtree, "+
					"instead of for-range over []ast.%s + type switch", elemKind),
			})
		})
	})
	return diags
}

// typeSwitchOperand returns the operand X of a type-switch's `x := X.(type)`
// or `X.(type)` assign-stmt. Returns nil if the assign-stmt shape is unexpected.
func typeSwitchOperand(stmt ast.Stmt) ast.Expr {
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		if len(s.Rhs) != 1 {
			return nil
		}
		ta, ok := s.Rhs[0].(*ast.TypeAssertExpr)
		if !ok {
			return nil
		}
		return ta.X
	case *ast.ExprStmt:
		ta, ok := s.X.(*ast.TypeAssertExpr)
		if !ok {
			return nil
		}
		return ta.X
	}
	return nil
}

// helpers — pure go/types operations, shared between production and fixture.

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// namedTypeImportPath returns the import path of the package that declares
// the named type underlying t. Handles three wrappings:
//   - *types.Pointer (one level)
//   - *types.Named (the base case)
//   - *types.TypeParam (generic constraint): when a method is called on a
//     generic-bound value (e.g. `func [F fs.ReadDirFS](fsys F) { fsys.ReadDir(...) }`),
//     the receiver's static type is *types.TypeParam — its constraint's
//     core type or sole named-interface embedding identifies the package.
//
// Returns "" if t resolves to none of these.
func namedTypeImportPath(t types.Type) string {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if tp, ok := t.(*types.TypeParam); ok {
		// Walk the constraint interface for an embedded named type from a
		// banned package. types.TypeParam.Constraint() returns *types.Interface
		// (possibly via Underlying for aliases).
		if iface, ok := tp.Constraint().Underlying().(*types.Interface); ok {
			for i := 0; i < iface.NumEmbeddeds(); i++ {
				if path := namedTypeImportPath(iface.EmbeddedType(i)); path != "" {
					return path
				}
			}
		}
		return ""
	}
	named, ok := t.(*types.Named)
	if !ok {
		return ""
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	return obj.Pkg().Path()
}

// astListElemKind returns "Decl"|"Spec"|"Stmt"|"Expr" if expr's static type
// is []ast.<Kind> from package go/ast; otherwise ("", false).
func astListElemKind(info *types.Info, expr ast.Expr) (string, bool) {
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return "", false
	}
	slice, ok := tv.Type.Underlying().(*types.Slice)
	if !ok {
		return "", false
	}
	elem := slice.Elem()
	named, ok := elem.(*types.Named)
	if !ok {
		return "", false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != "go/ast" {
		return "", false
	}
	switch obj.Name() {
	case "Decl", "Spec", "Stmt", "Expr":
		return obj.Name(), true
	}
	return "", false
}

// identNameOf returns the name of expr if it is a plain *ast.Ident, else "".
func identNameOf(expr ast.Expr) string {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// exprRepr returns a stable string representation of an AST expression using
// go/printer. Used to compare whether two expressions refer to the same
// sub-tree (e.g. IndexExpr.X vs. the RangeStmt's X in form (c) detection).
// A fresh token.FileSet is used so that position differences do not affect the
// printed output — only the expression shape matters.
//
// types.Identical is not applicable here: we compare expression shape
// (variable name / selector path), not type equality. A fresh FileSet ensures
// position bytes do not affect the printed output.
func exprRepr(e ast.Expr) string {
	if e == nil {
		return ""
	}
	var sb strings.Builder
	_ = printer.Fprint(&sb, token.NewFileSet(), e)
	return sb.String()
}

// isStarAstNodeType reports whether expr's static type is *go/ast.<Kind>
// (a concrete pointer to a struct in package go/ast).
func isStarAstNodeType(info *types.Info, expr ast.Expr) bool {
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	ptr, ok := tv.Type.(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == "go/ast"
}

// pkgFileRel returns the file path relative to modRoot for a *ast.File whose
// position is owned by pkg.Fset.
func pkgFileRel(modRoot string, pkg *packages.Package, file *ast.File) string {
	pos := pkg.Fset.Position(file.Pos())
	if pos.Filename == "" {
		return ""
	}
	abs, err := filepath.Abs(pos.Filename)
	if err != nil {
		return filepath.ToSlash(pos.Filename)
	}
	rel, err := filepath.Rel(modRoot, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// runFixture parses src, type-checks it via importer.Default(), and runs the
// path-A/A'/B pure functions. Returns the combined diagnostic list. Used by
// TestScannerFrameworkUsage01_Fixture to lock the rule's behavior to a set
// of inline source samples.
//
// Fixture src must import only stdlib packages because importer.Default()
// cannot load module-private packages. (The live rule uses
// typeseval.SharedResolver, which loads the whole module via packages.Load
// — the two paths share the same pure functions but differ in type-check
// infrastructure.)
//
// Type-check errors are collected silently: fixture src is intentionally
// minimal and not always semantically pure (e.g. unused imports, function
// values dropped on the floor), but the AST + types.Info are still good
// enough for the path-A/A'/B detection logic.
func runFixture(t *testing.T, src string) []scanner.Diagnostic {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fake.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Defs:  map[*ast.Ident]types.Object{},
		Uses:  map[*ast.Ident]types.Object{},
	}
	cfg := &types.Config{
		Importer: importer.Default(),
		Error:    func(error) {}, // partial type info acceptable
	}
	_, _ = cfg.Check("fake", fset, []*ast.File{file}, info)
	var diags []scanner.Diagnostic
	diags = append(diags, forbiddenWalkRefs(info, fset, file, "fake.go")...)
	diags = append(diags, forbiddenAstListTypeAssertions(info, fset, file, "fake.go")...)
	return diags
}

// TestScannerFrameworkUsage01_Fixture exercises forbiddenWalkRefs and
// forbiddenAstListTypeAssertions directly via parsed-from-string fixtures.
// 30 cases cover every AST shape the live rule must catch (path A: 12, path
// A': 4, path B: 10, form-(c) escape hatches: 3) plus 6 negative shapes
// scattered within path B, plus 1 standalone negative.
// Because both the live rule and this fixture call the same pure functions,
// they cannot drift.
func TestScannerFrameworkUsage01_Fixture(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		src      string
		wantHits int
	}{
		// ============ Path A: package-level symbol calls ============

		{
			name: "dot_import_path_filepath",
			src: `package fake
import . "path/filepath"
func _() string { return Join("a", "b") }
`,
			wantHits: 1,
		},
		{
			name: "dot_import_os",
			src: `package fake
import . "os"
func _() error { _, err := ReadDir("/tmp"); return err }
`,
			wantHits: 1,
		},
		{
			name: "dot_import_io_fs",
			src: `package fake
import . "io/fs"
func _() error { return WalkDir(nil, ".", nil) }
`,
			wantHits: 1,
		},
		{
			name: "direct_call_filepath_walkdir",
			src: `package fake
import "path/filepath"
func _() error { return filepath.WalkDir(".", nil) }
`,
			wantHits: 1,
		},
		{
			name: "function_value_binding",
			src: `package fake
import "path/filepath"
func _() { fp := filepath.WalkDir; _ = fp }
`,
			wantHits: 1,
		},
		{
			name: "argument_passing",
			src: `package fake
import "path/filepath"
func consume(any) {}
func _() { consume(filepath.WalkDir) }
`,
			wantHits: 1,
		},
		{
			name: "ioutil_readdir_call",
			src: `package fake
import "io/ioutil"
func _() error { _, err := ioutil.ReadDir("/tmp"); return err }
`,
			wantHits: 1,
		},
		{
			name: "renamed_import_alias_call",
			src: `package fake
import iofs "io/fs"
func _() error { return iofs.WalkDir(nil, ".", nil) }
`,
			wantHits: 1,
		},
		{
			name: "dot_import_go_ast",
			src: `package fake
import . "go/ast"
func _() { Inspect(nil, nil) }
`,
			wantHits: 1,
		},
		{
			name: "direct_call_ast_inspect",
			src: `package fake
import "go/ast"
func _() { ast.Inspect(nil, nil) }
`,
			wantHits: 1,
		},
		{
			name: "direct_call_ast_walk",
			src: `package fake
import "go/ast"
func _() { ast.Walk(nil, nil) }
`,
			wantHits: 1,
		},
		{
			name: "direct_call_inspector_new",
			src: `package fake
import "golang.org/x/tools/go/ast/inspector"
func _() { _ = inspector.New(nil) }
`,
			wantHits: 1,
		},

		// ============ Path A': method calls on banned receiver types ============

		{
			name: "method_call_os_file_readdir",
			src: `package fake
import "os"
func _() error {
	f, err := os.Open("/")
	if err != nil { return err }
	_, err = f.ReadDir(-1) // (*os.File).ReadDir — banned via path A'
	return err
}
`,
			wantHits: 1,
		},
		{
			name: "method_call_fs_readdirfs_readdir",
			src: `package fake
import "io/fs"
func _(fsys fs.ReadDirFS) error {
	_, err := fsys.ReadDir(".") // (fs.ReadDirFS).ReadDir — banned via path A'
	return err
}
`,
			wantHits: 1,
		},
		{
			// Generic TypeParam constraint: receiver is a type parameter bound
			// by fs.ReadDirFS — namedTypeImportPath must descend into the
			// constraint interface to find the banned package, otherwise the
			// generic form silently bypasses path A'.
			name: "method_call_generic_typeparam_bypass",
			src: `package fake
import "io/fs"
func _[F fs.ReadDirFS](fsys F) error {
	_, err := fsys.ReadDir(".") // F.ReadDir resolves to (fs.ReadDirFS).ReadDir
	return err
}
`,
			wantHits: 1,
		},
		{
			// Generic API form (Go 1.23+): inspector.All[*ast.X] produces a
			// SelectorExpr `inspector.All` wrapped by IndexExpr; SelectorExpr
			// scan still sees `inspector.All` and path A flags it.
			name: "inspector_all_generic_form",
			src: `package fake
import (
	"go/ast"
	"golang.org/x/tools/go/ast/inspector"
)
func _(insp *inspector.Inspector) {
	for n := range inspector.All[*ast.FuncDecl](insp) {
		_ = n
	}
}
`,
			wantHits: 1,
		},

		// ============ Path B: for-range over []ast.X + type assertion ============

		{
			name: "for_range_decls_func_decl",
			src: `package fake
import "go/ast"
func _(file *ast.File) {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			_ = fn
		}
	}
}
`,
			wantHits: 1,
		},
		{
			name: "for_range_specs_type_spec",
			src: `package fake
import "go/ast"
func _(gd *ast.GenDecl) {
	for _, spec := range gd.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok { continue }
		_ = ts
	}
}
`,
			wantHits: 1,
		},
		{
			name: "for_range_body_list_assign",
			src: `package fake
import "go/ast"
func _(body *ast.BlockStmt) {
	for _, stmt := range body.List {
		as, ok := stmt.(*ast.AssignStmt)
		if !ok { continue }
		_ = as
	}
}
`,
			wantHits: 1,
		},
		{
			name: "for_range_imports_no_assertion",
			src: `package fake
import "go/ast"
func _(file *ast.File) {
	for _, imp := range file.Imports {
		_ = imp.Path.Value
	}
}
`,
			wantHits: 0,
		},
		{
			name: "for_range_decls_no_ast_target",
			src: `package fake
import "go/ast"
type MyDecl struct { ast.Decl }
func _(file *ast.File) {
	for _, decl := range file.Decls {
		if md, ok := decl.(*MyDecl); ok {
			_ = md
		}
	}
}
`,
			wantHits: 0, // *MyDecl is not in package go/ast — path B excludes
		},
		{
			// Path B form (c): paired-index `for i := range Y { Y[i].(*ast.W) }`
			// is now flagged. Previously exempted by rs.Value == nil early-return;
			// that exemption is removed now that EachInChildren provides a typed
			// children-only API making paired-index iteration unnecessary.
			name: "for_range_index_no_binding_flagged",
			src: `package fake
import "go/ast"
func _(file *ast.File) {
	for i := range file.Decls {
		if _, ok := file.Decls[i].(*ast.FuncDecl); ok {
			_ = i
		}
	}
}
`,
			wantHits: 1,
		},
		{
			// Negative: paired-index but ta.Type is not *ast.<Kind> — path B
			// form (c) must not flag non-ast type assertions.
			name: "for_range_index_non_ast_target_passes",
			src: `package fake
import "go/ast"
type MyNode struct{}
func _(file *ast.File) {
	for i := range file.Decls {
		if _, ok := file.Decls[i].(*MyNode); ok {
			_ = i
		}
	}
}
`,
			wantHits: 0, // *MyNode is not in package go/ast — form (c) excludes
		},
		{
			// Negative: paired-index but rs.X is not []ast.<Kind> — path B
			// form (c) must not flag non-ast-list range loops.
			name: "for_range_index_non_ast_list_passes",
			src: `package fake
import "go/ast"
func _(ints []int) {
	for i := range ints {
		_ = ints[i]
		_ = (*ast.File)(nil) // use ast to avoid import error
	}
}
`,
			wantHits: 0, // []int is not []ast.<Kind> — form (c) excludes
		},
		{
			// Form (b): TypeSwitchStmt is the same kind-dispatch semantically
			// as TypeAssertExpr — must also be flagged so AI cannot bypass
			// path B by writing `switch decl := decl.(type) { case *ast.X }`.
			name: "for_range_decls_type_switch_func_decl",
			src: `package fake
import "go/ast"
func _(file *ast.File) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			_ = d
		}
	}
}
`,
			wantHits: 1,
		},
		{
			// Negative: same TypeSwitch shape but no *ast.<Kind> case clause —
			// the runtime dispatch is on a non-ast type, path B should not
			// flag it.
			name: "for_range_decls_type_switch_no_ast_case",
			src: `package fake
import "go/ast"
type MyDecl struct{ ast.Decl }
func _(file *ast.File) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *MyDecl:
			_ = d
		}
	}
}
`,
			wantHits: 0,
		},

		// ============ Negatives (must NOT hit any path) ============

		{
			name: "named_import_non_banned_symbol",
			src: `package fake
import "path/filepath"
func _() string { return filepath.Join("a", "b") }
`,
			wantHits: 0, // Join is not banned
		},

		// ============ Form (c) companion-index escape hatch — F4 ============

		{
			// F4: companion-index via another slice indexed with i — form (c) must
			// NOT flag when a second distinct slice is also indexed by the same
			// loop variable. The loop has LHS/RHS pairing semantics and cannot be
			// replaced by EachInChildren; the companion-index guard fires.
			name: "for_range_paired_index_companion_index_expr_passes",
			src: `package fake
import "go/ast"
func _(stmt *ast.AssignStmt) {
	for i := range stmt.Lhs {
		_ = stmt.Lhs[i].(*ast.Ident)
		_ = stmt.Rhs[i]
	}
}
`,
			wantHits: 0, // companion-index guard: stmt.Rhs[i] pairs with stmt.Lhs[i] → not flagged
		},
		{
			// F4: companion-index via CallExpr.Args passing index i — form (c) must
			// NOT flag when the loop index is passed as a bare argument to a call
			// (the callee may do OtherSlice[i] internally; pairing semantics).
			name: "for_range_paired_index_companion_call_arg_passes",
			src: `package fake
import "go/ast"
func process(decls []ast.Decl, i int) {}
func _(file *ast.File) {
	for i := range file.Decls {
		_ = file.Decls[i].(*ast.FuncDecl)
		process(file.Decls, i)
	}
}
`,
			wantHits: 0, // companion-index guard: i passed as call arg → not flagged
		},

		// ============ Form (c) intermediate-variable escape hatch — F10 ============

		{
			// F10: intermediate variable decouples the index from the type assertion.
			// form (c) checks for `Y[i].(*ast.W)` — i.e. TypeAssertExpr.X must be
			// an IndexExpr. When the caller writes `decl := file.Decls[i]; decl.(*ast.FuncDecl)`,
			// ta.X is the Ident `decl`, not an IndexExpr — form (c) does not fire.
			// This is an intentional escape hatch: intermediate variable makes the
			// pairing intent explicit and is a valid GoCell pattern when the variable
			// name clarifies meaning.
			name: "for_range_index_with_intermediate_var_passes",
			src: `package fake
import "go/ast"
func _(file *ast.File) {
	for i := range file.Decls {
		decl := file.Decls[i]
		if d, ok := decl.(*ast.FuncDecl); ok { _ = d }
	}
}
`,
			wantHits: 0, // intermediate variable: ta.X is Ident not IndexExpr → form (c) does not fire
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := runFixture(t, tc.src)
			if len(diags) != tc.wantHits {
				t.Errorf("got %d hits, want %d (diags=%v)", len(diags), tc.wantHits, diags)
			}
		})
	}
}
