package archtest

//   - INVARIANT: SCANNER-FRAMEWORK-USAGE-01
//   - INVARIANT: SCANNER-FRAMEWORK-USAGE-02
//
// scanner_framework_usage_test.go — guard archtest tools/archtest/*_test.go
// from bypassing the shared scanner framework at tools/archtest/internal/scanner.
//
//   - USAGE-01: must not bypass the framework via raw ast/fs/inspector walks.
//   - USAGE-02: must not hand-roll the closure+done/found sentinel idiom over
//     scanner.EachInChildren to fake find-first-and-stop; use the typed funnel
//     scanner.FindFirstChild[N] instead (allowlist = 0).
//
// Two related SCANNER-* invariants share this theme file. Per
// .claude/rules/gocell/ai-collab.md "## archtest 文件命名", promote to
// {theme}_invariants_test.go (scanner_framework_invariants_test.go) if a
// third related SCANNER-* invariant accumulates.

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
//	Path A — references to banned package-level symbols in three forms:
//	    A.1 dot-import declarations:        `import . "<banned>"`
//	    A.2 qualified call sites:           `banned.Func(...)` / `banned.Func` value ref
//	    A.3 dot-imported bare-Ident refs:   `Func(...)` after `import . "banned"`
//	  Banned import paths and symbols (top-level functions only — methods
//	  on banned receiver types are caught separately by path A' /
//	  forbiddenMethodSymbols):
//	    path/filepath: WalkDir, Walk, Glob
//	    os:            ReadDir
//	    io/ioutil:     ReadDir   (deprecated but still callable)
//	    io/fs:         WalkDir, Walk, Glob, ReadDir
//	    go/ast:                              Inspect, Walk, Preorder
//	    golang.org/x/tools/go/ast/inspector: New, All
//	  A.2/A.3 share a single typeseval.ResolvePackageRef resolver. A.3 closes
//	  PR445-FU-TYPEAWARE-CALL-MATCHER-IDENT-01 (PR-TS2): pre-PR-TS2 the bare
//	  call site was protected only by A.1 (dot-import declaration scan).
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
//   - SelectorExpr / Ident scans via scanner.EachInSubtree (dogfood — the
//     framework's first consumer is the rule that enforces it).
//   - Path A.1: dot-import declaration scan flags `import . "<pkg>"` directly.
//   - Path A.2: SelectorExpr scan + typeseval.ResolvePackageRef returns
//     (pkgPath, name) syntactically from info.Uses[sel.X] → *types.PkgName
//     plus sel.Sel.Name; tolerates partial type info for non-stdlib imports.
//   - Path A.3: Ident scan + typeseval.ResolvePackageRef resolves bare `Func`
//     (dot-imported) via info.Uses → *types.Func; Sel idents are pre-collected
//     and skipped so qualified `pkg.Func` (A.2) and method calls do not
//     double-count.
//   - Path A': SelectorExpr scan + typeseval.ResolveMethodCall reads
//     info.Selections.Obj() to recover the dispatched *types.Func. Covers
//     MethodVal (`recv.Method()`) and MethodExpr (`T.Method(recv, ...)`)
//     across pointer types, interface types, promoted via struct embedding,
//     named-type definitions, type aliases, and generic type-parameter
//     constraints (including multi-embed). The dispatched method's owning
//     package replaces the prior sel.X-static-type walker.
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
		if pkg == nil {
			t.Fatalf("typeseval.SharedResolver returned nil package " +
				"(SharedResolver invariant broken)")
		}
		if pkg.TypesInfo == nil || pkg.Fset == nil {
			t.Fatalf("package %q loaded without TypesInfo/Fset "+
				"(SharedResolver misconfigured — full type info is required "+
				"for forbiddenWalkRefs/forbiddenAstListTypeAssertions)", pkg.PkgPath)
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

// TestScannerFrameworkUsage01_InspectorMethodBanLive locks the
// forbiddenMethodSymbols[golang.org/x/tools/go/ast/inspector] data row by
// loading a controlled fixture sub-package via packages.Load (which can
// resolve non-stdlib imports — importer.Default() in runFixture cannot) and
// asserting forbiddenWalkRefs emits one diagnostic per banned method call.
//
// Coverage: the fixture package tools/archtest/internal/inspectorredfixture
// contains exactly 4 (*inspector.Inspector) method calls — Preorder, Nodes,
// WithStack, PreorderSeq. Removing the inspector entry from
// forbiddenMethodSymbols turns this test red (got 0, want 4), so the data
// row is locked Hard at the rule-pipeline level (not just data-snapshot).
//
// The fixture sub-package is out of scope of SCANNER-FRAMEWORK-USAGE-01's
// own live scan (parent-dir filter at line 120) so the banned calls there
// do not pollute the production rule.
func TestScannerFrameworkUsage01_InspectorMethodBanLive(t *testing.T) {
	root := findModuleRoot(t)
	// includeTests=false: the inspectorredfixture package has no _test.go files
	// so loading tests would only add no-op work. The archtest_fixture build
	// tag is required because inspector_red.go is gated behind it (sister
	// fixture convention — see wrapfixture/violation, rawparamfixture); without
	// the tag packages.Load returns an empty package and the test fails red on
	// got=0 want=4.
	resolver, err := typeseval.SharedResolver(root, false, []string{"archtest_fixture"}, "./tools/archtest/internal/inspectorredfixture")
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
			diags = append(diags, forbiddenWalkRefs(pkg.TypesInfo, pkg.Fset, file, rel)...)
		}
	}

	const wantHits = 4
	if len(diags) != wantHits {
		for i, d := range diags {
			t.Logf("diag[%d]: %s:%d: %s", i, d.Rel, d.Line, d.Message)
		}
		t.Fatalf("forbiddenMethodSymbols[inspector] coverage: got %d diags, want %d", len(diags), wantHits)
	}
	bannedMethods := map[string]bool{"Preorder": true, "Nodes": true, "WithStack": true, "PreorderSeq": true}
	for _, d := range diags {
		matched := false
		for m := range bannedMethods {
			if strings.Contains(d.Message, "."+m) {
				matched = true
				delete(bannedMethods, m)
				break
			}
		}
		if !matched {
			t.Errorf("unexpected diag (none of Preorder/Nodes/WithStack/PreorderSeq matched): %q", d.Message)
		}
	}
	if len(bannedMethods) > 0 {
		t.Errorf("missing diag(s) for methods: %v", bannedMethods)
	}
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
	"golang.org/x/tools/go/ast/inspector": {"New", "All"},
}

// forbiddenMethodSymbols maps banned receiver-type import paths to the
// method names that must not be invoked on values of named types from those
// packages. Resolved via go/types Info — covers
//
//	(*os.File).ReadDir
//	(fs.FS).ReadDir / (fs.ReadDirFS).ReadDir / (fs.GlobFS).Glob / WalkDir variants
//	(*inspector.Inspector).Preorder / .Nodes / .WithStack / .PreorderSeq
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
	"os":                                  {"ReadDir"},
	"io/fs":                               {"ReadDir", "WalkDir", "Glob"},
	"golang.org/x/tools/go/ast/inspector": {"Preorder", "Nodes", "WithStack", "PreorderSeq"},
}

// forbiddenWalkRefs reports any reference to a banned package-level symbol or
// receiver-type method, with three sub-paths:
//
//	(1)  dot-import declaration scan — flags `import . "<banned>"` at the import.
//	(2b) SelectorExpr scan — qualified calls `pkg.Func` (path A.2) resolved via
//	     typeseval.ResolvePackageRef; receiver-type method calls (path A')
//	     resolved via typeseval.ResolveMethodCall (info.Selections-based, so
//	     promoted methods via struct embedding and named-type definitions of
//	     banned interfaces are recovered, not just direct interface/pointer
//	     receivers).
//	(2c) Bare-Ident scan — dot-imported call/value references `Func` (path A.3)
//	     resolved via typeseval.ResolvePackageRef. SelectorExpr.Sel idents are
//	     pre-collected and skipped (2a) so qualified `pkg.Func` and method calls
//	     `recv.Method` do not double-count.
//
// Signature: minimal type-info dependency `(*types.Info, *token.FileSet,
// *ast.File, rel)`. Production callers pass (pkg.TypesInfo, pkg.Fset, file,
// pkgFileRel(...)); fixture callers pass (minimalCheck.Info, fset, file,
// "fake.go"). Same pure function for both — fixture/prod cannot drift.
//
// Iteration uses scanner.EachInSubtree (dogfood — the rule that enforces the
// framework is itself implemented in the framework).
//
// ref: golang/tools go/analysis/passes/copylock/copylock.go — qualified
//
//	identifier resolution via info.Uses[id].(*types.PkgName)
func forbiddenWalkRefs(info *types.Info, fset *token.FileSet, file *ast.File, rel string) []scanner.Diagnostic {
	var out []scanner.Diagnostic

	// (1) Dot-import declaration scan.
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

	// (2a) Pre-collect Sel idents of every SelectorExpr so the bare-Ident scan
	// (2c) can skip them. Sel positions are matched in (2b) as part of their
	// owning SelectorExpr; visiting them again would double-count.
	selSels := make(map[*ast.Ident]bool)
	scanner.EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel != nil {
			selSels[sel.Sel] = true
		}
	})

	// (2b) SelectorExpr scan: path A.2 (qualified call) + path A' (receiver
	// method).
	scanner.EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if path, name, ok := typeseval.ResolvePackageRef(info, sel); ok {
			if symbols, banned := forbiddenWalkSymbols[path]; banned && contains(symbols, name) {
				out = append(out, scanner.Diagnostic{
					Rel:     rel,
					Line:    fset.Position(sel.Pos()).Line,
					Message: fmt.Sprintf("use tools/archtest/internal/scanner instead of %s.%s", path, name),
				})
				return
			}
		}
		// Method call on banned receiver type: (*os.File).ReadDir,
		// (fs.FS).WalkDir, promoted via struct embedding, named-type definition
		// of a banned interface, generic type-parameter constrained by a banned
		// interface, etc. typeseval.ResolveMethodCall recovers the actual method
		// object via info.Selections so the dispatch source is preserved across
		// all these AST shapes (the prior NamedTypeImportPath walker only saw
		// sel.X's static type and missed promoted/named-def cases — closed by
		// PR469-review-round-2).
		if fn, ok := typeseval.ResolveMethodCall(info, sel); ok {
			if methods, banned := forbiddenMethodSymbols[fn.Pkg().Path()]; banned && contains(methods, fn.Name()) {
				out = append(out, scanner.Diagnostic{
					Rel:     rel,
					Line:    fset.Position(sel.Pos()).Line,
					Message: fmt.Sprintf("use tools/archtest/internal/scanner instead of (%s).%s", fn.Pkg().Path(), fn.Name()),
				})
			}
		}
	})

	// (2c) Bare-Ident scan: path A.3 (dot-imported function reference).
	scanner.EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
		if selSels[id] {
			return
		}
		path, name, ok := typeseval.ResolvePackageRef(info, id)
		if !ok {
			return
		}
		if symbols, banned := forbiddenWalkSymbols[path]; banned && contains(symbols, name) {
			out = append(out, scanner.Diagnostic{
				Rel:     rel,
				Line:    fset.Position(id.Pos()).Line,
				Message: fmt.Sprintf("use tools/archtest/internal/scanner instead of %s.%s", path, name),
			})
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
//     TypeAssertExpr in body is IndexExpr(Y, i).(*ast.<Kind>), OR the type
//     assertion targets an intermediate variable trivially aliased to Y[i]
//     via `tmp := Y[i]` inside rs.Body (single-level alias only).
//
// Form (c) was previously exempted by the `rs.Value == nil` early-return; that
// exemption is removed now that EachInChildren provides a typed children-only
// API that makes paired-index iteration unnecessary. The intermediate-variable
// path closes a trivial AST rewrite that would otherwise let AI bypass form
// (c) by writing `tmp := Y[i]; tmp.(*ast.W)` instead of the direct form.
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

			// Collect intermediate variable aliases for Y[indexName] introduced
			// in rs.Body via `tmp := Y[i]` (token.DEFINE, single LHS+RHS,
			// matching index variable and same slice as rs.X). This closes the
			// trivial AST rewrite that would otherwise let AI bypass form (c)
			// by writing `tmp := Y[i]; tmp.(*ast.W)` instead of the direct
			// form. Double-aliasing (`tmp1 := Y[i]; tmp2 := tmp1; tmp2.(*ast.W)`)
			// is intentionally not chased — single-level alias is the natural
			// rewrite; deeper chains are corner.
			intermediateAliases := map[string]struct{}{}
			scanner.EachInSubtree[ast.AssignStmt](rs.Body, func(asgn *ast.AssignStmt) {
				if asgn.Tok != token.DEFINE {
					return
				}
				if len(asgn.Lhs) != 1 || len(asgn.Rhs) != 1 {
					return
				}
				lhsId, ok := asgn.Lhs[0].(*ast.Ident)
				if !ok {
					return
				}
				idx, ok := asgn.Rhs[0].(*ast.IndexExpr)
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
				intermediateAliases[lhsId.Name] = struct{}{}
			})

			scanner.EachInSubtree[ast.TypeAssertExpr](rs.Body, func(ta *ast.TypeAssertExpr) {
				if ta.Type == nil {
					return
				}
				if !isStarAstNodeType(info, ta.Type) {
					return
				}
				// Direct form: `Y[i].(*ast.W)` — ta.X is the IndexExpr.
				if idx, ok := ta.X.(*ast.IndexExpr); ok {
					idxId, ok := idx.Index.(*ast.Ident)
					if !ok || idxId.Name != indexName {
						return
					}
					if exprRepr(idx.X) != rsXRepr {
						return
					}
				} else if id, ok := ta.X.(*ast.Ident); ok {
					// Intermediate-alias form: `tmp := Y[i]; tmp.(*ast.W)`.
					if _, found := intermediateAliases[id.Name]; !found {
						return
					}
				} else {
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
				_, caseHits = scanner.FindFirstChild[ast.CaseClause](ts.Body, func(cc *ast.CaseClause) bool {
					for j := range cc.List {
						if isStarAstNodeType(info, cc.List[j]) {
							return true
						}
					}
					return false
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
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
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
// 39 cases cover every AST shape the live rule must catch (path A
// qualified/dot-import declarations + bare-Ident, path A' method calls on
// banned receiver types, path B for-range over []ast.X with type assertions
// including paired-index + intermediate-alias rewrite + companion-index
// escape hatches, plus negative shapes proving the precision guards).
// Because both the live rule and this fixture call the same pure functions,
// they cannot drift.
//
// Inspector method-call coverage (forbiddenMethodSymbols
// `golang.org/x/tools/go/ast/inspector` entry) is locked separately by
// TestScannerFrameworkUsage01_InspectorMethodBanLive, which uses
// packages.Load to resolve the non-stdlib `golang.org/x/tools/...` import
// path that importer.Default() in runFixture cannot load.
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
			// Hit breakdown: (1) dot-import declaration scan = 1, (2b)
			// SelectorExpr scan = 0 (bare call has no SelectorExpr), (2c)
			// bare-Ident scan via typeseval.ResolvePackageRef = 1.
			name: "dot_import_os",
			src: `package fake
import . "os"
func _() error { _, err := ReadDir("/tmp"); return err }
`,
			wantHits: 2,
		},
		{
			// Hit breakdown: (1) = 1, (2b) = 0, (2c) = 1.
			name: "dot_import_io_fs",
			src: `package fake
import . "io/fs"
func _() error { return WalkDir(nil, ".", nil) }
`,
			wantHits: 2,
		},
		{
			// Bare-Ident scan must catch dot-imported call to a forbidden symbol
			// even when the import declaration is already flagged. Closes the
			// PR445-FU-TYPEAWARE-CALL-MATCHER-IDENT-01 dogfood gap.
			// Hit breakdown: (1) = 1, (2b) = 0, (2c) = 1.
			name: "dot_import_filepath_bare_walkdir_call",
			src: `package fake
import . "path/filepath"
func _() error { return WalkDir(".", nil) }
`,
			wantHits: 2,
		},
		{
			// Bare-Ident scan must NOT fire on type-only references through a
			// dot-import (no Func resolution). Only the import declaration warns.
			// Hit breakdown: (1) = 1, (2b) = 0, (2c) = 0 (FS resolves to TypeName, helper returns false).
			name: "dot_import_type_reference_only",
			src: `package fake
import . "io/fs"
var _ FS // type-only reference; no func call
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
			// Hit breakdown: (1) = 1, (2b) = 0, (2c) = 1.
			name: "dot_import_go_ast",
			src: `package fake
import . "go/ast"
func _() { Inspect(nil, nil) }
`,
			wantHits: 2,
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
			// by fs.ReadDirFS — typeseval.ResolveMethodCall reads
			// info.Selections.Obj() which returns the interface's method
			// regardless of the receiver's static *types.TypeParam wrapping.
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
			// PR469-review-round-2 P1 closure: struct embedding promotes
			// (fs.ReadDirFS).ReadDir into Wrap. info.Types[sel.X] would yield
			// *Wrap (current pkg) which has no entry in forbiddenMethodSymbols;
			// info.Selections.Obj() gives the actual *types.Func from io/fs.
			name: "method_call_promoted_via_struct_embedding",
			src: `package fake
import "io/fs"
type Wrap struct{ fs.ReadDirFS }
func _(w *Wrap) error { _, err := w.ReadDir("."); return err }
`,
			wantHits: 1,
		},
		{
			// Named-type definition of a banned interface. MyFS has the same
			// method set as fs.ReadDirFS; the Selection's Obj() points back to
			// the interface's *types.Func, so the io/fs package is recovered.
			name: "method_call_named_type_def_of_banned_interface",
			src: `package fake
import "io/fs"
type MyFS fs.ReadDirFS
func _(x MyFS) error { _, err := x.ReadDir("."); return err }
`,
			wantHits: 1,
		},
		{
			// PR469-review-round-3 P1: method expression `fs.ReadDirFS.ReadDir(fsys, ".")`.
			// info.Selections[sel].Kind() is MethodExpr (not MethodVal); the
			// resolver must accept both kinds. sel.X is the qualified interface
			// type SelectorExpr `fs.ReadDirFS`, not a value.
			name: "method_expr_qualified_interface",
			src: `package fake
import "io/fs"
func _(fsys fs.ReadDirFS) error {
	_, err := fs.ReadDirFS.ReadDir(fsys, ".")
	return err
}
`,
			wantHits: 1,
		},
		{
			// PR469-review-round-3 P1: method expression `(*os.File).ReadDir(f, -1)`.
			// sel.X is ParenExpr(StarExpr(SelectorExpr os.File)); Selection.Kind() is MethodExpr.
			name: "method_expr_pointer_type",
			src: `package fake
import "os"
func _(f *os.File) error {
	_, err := (*os.File).ReadDir(f, -1)
	return err
}
`,
			wantHits: 1,
		},
		{
			// PR469-review-round-3 P1: multi-embed generic constraint. RWFS
			// embeds fs.ReadDirFS AND fs.GlobFS; each method resolves to its
			// own embedded interface. Selections.Obj() returns the correct
			// owning *types.Func per call (not just the first embed walked).
			name: "method_call_multi_embed_generic_constraint",
			src: `package fake
import "io/fs"
type RWFS interface{ fs.ReadDirFS; fs.GlobFS }
func _[F RWFS](fsys F) error {
	if _, err := fsys.ReadDir("."); err != nil { return err }
	_, err := fsys.Glob("*")
	return err
}
`,
			wantHits: 2,
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

		// ============ Form (c) intermediate-variable trivial rewrite — F10 (post-review hardening) ============

		{
			// Trivial AST rewrite: `tmp := Y[i]; tmp.(*ast.W)` is semantically
			// identical to `Y[i].(*ast.W)`. Before the post-review hardening,
			// form (c) only checked ta.X.(*ast.IndexExpr) and missed this
			// alias form — AI could mechanically rewrite paired-index into a
			// two-line alias to bypass the guard. The form (c) implementation
			// now collects single-level intermediate aliases via AssignStmt
			// (DEFINE token, single LHS+RHS, indexing rs.X with rs.Key) and
			// flags TypeAssertExpr on those aliases.
			name: "for_range_index_with_intermediate_var_flagged",
			src: `package fake
import "go/ast"
func _(file *ast.File) {
	for i := range file.Decls {
		decl := file.Decls[i]
		if d, ok := decl.(*ast.FuncDecl); ok { _ = d }
	}
}
`,
			wantHits: 1, // post-review: intermediate alias is a trivial rewrite, now flagged
		},
		{
			// Negative: intermediate variable is bound but never type-asserted —
			// no path B violation, even though the alias exists.
			name: "for_range_index_intermediate_var_no_type_assert_passes",
			src: `package fake
import "go/ast"
func _(file *ast.File) {
	for i := range file.Decls {
		decl := file.Decls[i]
		_ = decl
	}
}
`,
			wantHits: 0,
		},
		{
			// Negative: alias indexes a *different* slice (rs.X mismatch) —
			// intermediateAliases pruning checks exprRepr(idx.X) == rsXRepr,
			// so this Ident is not registered as an alias of file.Decls[i].
			name: "for_range_index_intermediate_var_different_slice_passes",
			src: `package fake
import "go/ast"
func _(file *ast.File, other []ast.Decl) {
	for i := range file.Decls {
		_ = file.Decls[i]
		alt := other[i]
		if d, ok := alt.(*ast.FuncDecl); ok { _ = d }
	}
}
`,
			wantHits: 0, // alt aliases other[i], not file.Decls[i] → not a form (c) violation for this range
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

// ============================================================================
// SCANNER-FRAMEWORK-USAGE-02
// ============================================================================

// INVARIANT: SCANNER-FRAMEWORK-USAGE-02
//
// archtest *_test.go files at tools/archtest/<file>_test.go must not hand-roll
// the closure+done/found sentinel idiom over scanner.EachInChildren or its
// 040 façade archtest.EachInChildren to fake find-first-and-stop:
//
//	found := false
//	scanner.EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
//		if found { return }            // ← early-return guard on a bool
//		if match(kv) { found = true }  // ← same bool set to literal true
//	})
//
// Use the typed funnel scanner.FindFirstChild[N] (or its façade
// archtest.FindFirstChild[N]) instead — the early-return is implicit, there
// is no caller-held flag, and the wrong N is a compile error. allowlist = 0:
// the migration is 100% complete, so the live scan over the whole archtest
// tree must return zero diagnostics; this is self-consistent with the
// migration (rule GREEN ⟺ all sites migrated).
//
// Callee-identity recognition (single Hard path): isMonitoredEachInChildren
// uses typeseval.ResolvePackageRef to resolve sel.Sel to its declaring package.
// The target set is {scannerPkgPath, archtestPkgPath}; both names are
// equivalent depth-1 walkers (archtest.EachInChildren is a thin façade around
// scanner.EachInChildren) and equally forbid the sentinel idiom.
//
// AI-rebust 双向锁评级:
//
//	下游 Hard: FindFirstChild — wrong N (interface vs *S) is a compile error
//	  via interface{*S; ast.Node}.
//	上游 Medium (= Go ceiling, terminal): archtest form-ban (allowlist 0).
//	  Cannot sealed-interface around "a user declares a bool"; EachInChildren
//	  must stay callable for pure iteration. Highest grade reachable in Go
//	  for this rule shape (structurally identical to PANIC-REGISTERED-01's
//	  honest caveat in .claude/rules/gocell/ai-collab.md: "the enforcement
//	  is archtest-bound, not compile-time ... the highest grade reachable in
//	  Go for this rule shape"). No upstream-Hard backlog opened (none
//	  reachable). FINDFIRSTINSUBTREE-API-01 is an orthogonal coverage axis
//	  (subtree find-first), not this funnel's hardening path — do not
//	  conflate.
//	Fixture-live anti-drift (Hard, 040 Stage 1.8): both
//	  TestScannerFrameworkUsage02 (live) and TestScannerFrameworkUsage02_Fixture
//	  (typed fixtures under tools/archtest/internal/usage02fixtures/) load
//	  source through the same typeseval.SharedResolver typed pipeline and
//	  invoke the same forbiddenClosureDoneSentinel pure detector with the
//	  same *types.Info — there is no syntactic fallback (the PR-505
//	  scannerLocalName + id-name fixture branch has been removed). A
//	  typeseval miss is a fixture bug, not a callee-identity ambiguity.
//
// Tool blind spots (godoc-declared scope of the chosen AST tooling —
// scanner.EachInSubtree[ast.CallExpr/IfStmt/AssignStmt/Ident] + typed
// callee resolution). Each has a reverse self-test in
// TestScannerFrameworkUsage02_BlindSpotReverse asserting it does NOT occur
// in production AST:
//
//	BS1: sentinel set via a non-`true`-literal RHS that is still a boolean
//	     (`done = ok`, `done = x == 1`) while used as `if done { return }`.
//	BS2: early-return expressed through the ELSE branch
//	     (`if !done { ... } else { return }`) instead of `if done { return }`.
//	BS3: guard/assignment split such that the sentinel is initialized to false
//	     before the EachInChildren call, the guard (`if <ident> { return }`)
//	     is inside the callback, but the `= true` assignment is OUTSIDE the
//	     callback FuncLit entirely (e.g. `done = true` appears after the
//	     EachInChildren call in the enclosing function body). This is a
//	     scoping limitation, not a real evasion: a functional find-first
//	     sentinel MUST set the flag from inside the iterating callback (the
//	     flag value depends on iteration); assignment outside the callback
//	     cannot implement find-first and is therefore not a functional guard
//	     shape. The BS3 reverse detector in closureDoneSentinelBlindSpots
//	     actively checks for this split form and asserts it does not appear
//	     in production; TestScannerFrameworkUsage02_BlindSpotForwardFixtures
//	     documents that the MAIN detector (forbiddenClosureDoneSentinel) by
//	     design returns 0 hits for the BS3 shape.
func TestScannerFrameworkUsage02(t *testing.T) {
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, true, nil, "./tools/archtest/...")
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}

	var diags []scanner.Diagnostic
	for _, pkg := range resolver.Packages() {
		if pkg == nil {
			t.Fatalf("typeseval.SharedResolver returned nil package " +
				"(SharedResolver invariant broken)")
		}
		if pkg.TypesInfo == nil || pkg.Fset == nil {
			t.Fatalf("package %q loaded without TypesInfo/Fset", pkg.PkgPath)
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			if filepath.ToSlash(filepath.Dir(rel)) != "tools/archtest" {
				continue
			}
			if !strings.HasSuffix(rel, "_test.go") {
				continue
			}
			diags = append(diags, forbiddenClosureDoneSentinel(pkg.TypesInfo, pkg.Fset, file, rel)...)
		}
	}
	scanner.Report(t, "SCANNER-FRAMEWORK-USAGE-02", diags)
}

// eachInChildrenCalleeSel returns the SelectorExpr `<pkg>.EachInChildren`
// from a generic call's Fun (IndexExpr / IndexListExpr base), or nil.
func eachInChildrenCalleeSel(call *ast.CallExpr) *ast.SelectorExpr {
	base := call.Fun
	switch idx := base.(type) {
	case *ast.IndexExpr:
		base = idx.X
	case *ast.IndexListExpr:
		base = idx.X
	}
	sel, ok := base.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "EachInChildren" {
		return nil
	}
	return sel
}

// isMonitoredEachInChildren reports whether sel resolves to either
// scannerPkgPath.EachInChildren (legacy direct call) or
// archtestPkgPath.EachInChildren (040 façade end-state, thin wrapper
// around scanner.EachInChildren). Both forms are equivalent depth-1
// walkers and equally forbid the closure+done sentinel idiom.
//
// Single Hard path: typeseval.ResolvePackageRef. There is no syntactic
// fallback — fixture and live scan share the same typed pipeline
// (TestScannerFrameworkUsage02_Fixture loads fixtures via
// typeseval.SharedResolver), so a typeseval miss is a fixture or
// detector bug, not a callee-identity ambiguity to paper over.
func isMonitoredEachInChildren(info *types.Info, sel *ast.SelectorExpr) bool {
	path, name, ok := typeseval.ResolvePackageRef(info, sel)
	if !ok || name != "EachInChildren" {
		return false
	}
	return path == scannerPkgPath || path == archtestPkgPath
}

// ifBodyHasDirectReturn reports whether ifStmt.Body has a ReturnStmt as a
// direct statement (the `if cond { return }` early-exit shape; nested
// returns inside inner blocks are not the guard form we detect).
func ifBodyHasDirectReturn(ifStmt *ast.IfStmt) bool {
	if ifStmt.Body == nil {
		return false
	}
	_, ok := scanner.FindFirstChild[ast.ReturnStmt](ifStmt.Body, func(*ast.ReturnStmt) bool { return true })
	return ok
}

// condIdentNames returns every identifier name appearing in cond (the bare
// `if x`, or operands of `if a || b`, `if !x`, `if x || f(y)` …).
func condIdentNames(cond ast.Expr) map[string]bool {
	names := map[string]bool{}
	if id, ok := cond.(*ast.Ident); ok {
		names[id.Name] = true
	}
	scanner.EachInSubtree[ast.Ident](cond, func(id *ast.Ident) {
		names[id.Name] = true
	})
	return names
}

// forbiddenClosureDoneSentinel flags scanner.EachInChildren or
// archtest.EachInChildren callbacks whose body contains BOTH (a) an
// early-return guard `if <sentinel> ... { return }` and (b)
// `<sentinel> = true`, i.e. the hand-rolled find-first sentinel.
func forbiddenClosureDoneSentinel(info *types.Info, fset *token.FileSet, file *ast.File, rel string) []scanner.Diagnostic {
	var out []scanner.Diagnostic
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel := eachInChildrenCalleeSel(call)
		if sel == nil || !isMonitoredEachInChildren(info, sel) {
			return
		}
		// The callback is the sole FuncLit direct child of the call (depth-1);
		// dogfood FindFirstChild rather than for-range over call.Args.
		cb, _ := scanner.FindFirstChild[ast.FuncLit](call, func(*ast.FuncLit) bool { return true })
		if cb == nil || cb.Body == nil {
			return
		}

		// (a) idents assigned a literal `true` inside the callback.
		assignedTrue := map[string]bool{}
		scanner.EachInSubtree[ast.AssignStmt](cb.Body, func(as *ast.AssignStmt) {
			if len(as.Lhs) != len(as.Rhs) {
				return
			}
			for i := range as.Lhs {
				lhs, ok := as.Lhs[i].(*ast.Ident)
				if !ok {
					continue
				}
				if rhs, ok := as.Rhs[i].(*ast.Ident); ok && rhs.Name == "true" {
					assignedTrue[lhs.Name] = true
				}
			}
		})
		if len(assignedTrue) == 0 {
			return
		}

		// (b) idents used as an early-return guard condition.
		flagged := false
		scanner.EachInSubtree[ast.IfStmt](cb.Body, func(ifStmt *ast.IfStmt) {
			if flagged || !ifBodyHasDirectReturn(ifStmt) {
				return
			}
			for name := range condIdentNames(ifStmt.Cond) {
				if assignedTrue[name] {
					flagged = true
					return
				}
			}
		})
		if flagged {
			out = append(out, scanner.Diagnostic{
				Rel:  rel,
				Line: fset.Position(call.Pos()).Line,
				Message: "closure+done/found sentinel over scanner.EachInChildren or " +
					"archtest.EachInChildren is forbidden (SCANNER-FRAMEWORK-USAGE-02): " +
					"use the typed funnel scanner.FindFirstChild[N] / " +
					"archtest.FindFirstChild[N](root, predicate) instead — the " +
					"early-return is implicit and there is no caller-held flag",
			})
		}
	})
	return out
}

// usage02Detector is the detector signature shared by forbiddenClosureDoneSentinel
// (main detector) and closureDoneSentinelBlindSpots (BS reverse detector).
type usage02Detector func(*types.Info, *token.FileSet, *ast.File, string) []scanner.Diagnostic

// loadFixture02 returns SCANNER-FRAMEWORK-USAGE-02 diagnostics for the named
// fixture file under tools/archtest/internal/usage02fixtures/<caseName>.go,
// using the same typed pipeline (typeseval.SharedResolver) as the live scan.
// caseName is the file basename without the .go suffix.
//
// Fixture and live thus share both the *types.Info source AND the pure detector
// function — no syntactic fallback drift surface. This is the single Hard path
// that closes the PR-505 fallback Soft blind spot (see TestScannerFrameworkUsage02
// godoc, 040 Stage 1.8 plan).
func loadFixture02(t *testing.T, caseName string, detector usage02Detector) []scanner.Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, true, nil, "./tools/archtest/...")
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}
	target := usage02FixturesRelDir + "/" + caseName + ".go"
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			if rel != target {
				continue
			}
			return detector(pkg.TypesInfo, pkg.Fset, file, rel)
		}
	}
	t.Fatalf("loadFixture02: fixture not found at %s; ensure the file exists "+
		"in tools/archtest/internal/usage02fixtures/ and the package is reachable "+
		"via ./tools/archtest/...", target)
	return nil
}

// TestScannerFrameworkUsage02_Fixture locks forbiddenClosureDoneSentinel to
// typed-loaded fixtures under tools/archtest/internal/usage02fixtures/. Both
// the live rule and this fixture call the same pure detector with the same
// *types.Info source, so they cannot drift.
func TestScannerFrameworkUsage02_Fixture(t *testing.T) {
	t.Parallel()
	cases := []struct {
		caseName string
		wantHits int
	}{
		{"red_scanner_done_sentinel", 1},
		{"red_scanner_found_disjunct", 1},
		{"red_archtest_done_sentinel", 1},
		{"green_scanner_findfirstchild", 0},
		{"green_scanner_pure_iteration", 0},
		{"green_scanner_eachinsubtree_existence", 0},
		{"green_scanner_no_true_assign", 0},
		{"green_archtest_findfirstchild", 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.caseName, func(t *testing.T) {
			t.Parallel()
			diags := loadFixture02(t, tc.caseName, forbiddenClosureDoneSentinel)
			if len(diags) != tc.wantHits {
				t.Errorf("got %d hits, want %d (diags=%v)", len(diags), tc.wantHits, diags)
			}
		})
	}
}

// closureDoneSentinelBlindSpots detects the BS1/BS2 evasion shapes (see
// TestScannerFrameworkUsage02 godoc). Used by the reverse self-test to
// assert these forms do NOT exist in production archtest AST — if one
// appears, forbiddenClosureDoneSentinel would silently miss it.
func closureDoneSentinelBlindSpots(info *types.Info, fset *token.FileSet, file *ast.File, rel string) []scanner.Diagnostic {
	var out []scanner.Diagnostic
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel := eachInChildrenCalleeSel(call)
		if sel == nil || !isMonitoredEachInChildren(info, sel) {
			return
		}
		// The callback is the sole FuncLit direct child of the call (depth-1);
		// dogfood FindFirstChild rather than for-range over call.Args.
		cb, _ := scanner.FindFirstChild[ast.FuncLit](call, func(*ast.FuncLit) bool { return true })
		if cb == nil || cb.Body == nil {
			return
		}

		// A blind-spot candidate must be a sentinel-shaped bool: initialized
		// to false via `x := false` or `var x bool` lexically BEFORE this
		// specific call within the enclosing function body, OR inside cb.Body
		// itself. Scoping to statements before call.Pos() prevents cross-call
		// pollution: a `done:=false` near a different EachInChildren call in
		// the same function does not bleed into this call's falseInit (F3 fix).
		falseInit := map[string]bool{}
		enclosing := enclosingFuncBody(file, call)

		collectFalseInitAssign := func(scope ast.Node, requireBeforeCall bool) {
			scanner.EachInSubtree[ast.AssignStmt](scope, func(as *ast.AssignStmt) {
				if requireBeforeCall && as.End() > call.Pos() {
					return
				}
				if as.Tok != token.DEFINE || len(as.Lhs) != len(as.Rhs) {
					return
				}
				for i := range as.Lhs {
					lhs, lok := as.Lhs[i].(*ast.Ident)
					rhs, rok := as.Rhs[i].(*ast.Ident)
					if lok && rok && rhs.Name == "false" {
						falseInit[lhs.Name] = true
					}
				}
			})
		}
		collectFalseInitSpec := func(scope ast.Node, requireBeforeCall bool) {
			scanner.EachInSubtree[ast.ValueSpec](scope, func(vs *ast.ValueSpec) {
				if requireBeforeCall && vs.End() > call.Pos() {
					return
				}
				tid, ok := vs.Type.(*ast.Ident)
				if !ok || tid.Name != "bool" || len(vs.Values) != 0 {
					return
				}
				for _, n := range vs.Names {
					falseInit[n.Name] = true
				}
			})
		}

		// Collect from enclosing function body (only stmts before this call).
		if enclosing != nil {
			collectFalseInitAssign(enclosing, true)
			collectFalseInitSpec(enclosing, true)
		}
		// Also collect from inside cb.Body itself (no position restriction).
		collectFalseInitAssign(cb.Body, false)
		collectFalseInitSpec(cb.Body, false)

		if len(falseInit) == 0 {
			return
		}

		assignedTrue := map[string]bool{}
		assignedNonLiteral := map[string]bool{}
		scanner.EachInSubtree[ast.AssignStmt](cb.Body, func(as *ast.AssignStmt) {
			if as.Tok != token.ASSIGN || len(as.Lhs) != len(as.Rhs) {
				return
			}
			for i := range as.Lhs {
				lhs, ok := as.Lhs[i].(*ast.Ident)
				if !ok || !falseInit[lhs.Name] {
					continue
				}
				if rhs, isID := as.Rhs[i].(*ast.Ident); isID && rhs.Name == "true" {
					assignedTrue[lhs.Name] = true
				} else {
					assignedNonLiteral[lhs.Name] = true
				}
			}
		})

		// BS3: guard ident used in `if <ident>...{return}` inside the callback,
		// NOT assigned `true` inside the callback, but IS assigned `true`
		// somewhere in the enclosing function body (outside the callback). This
		// is the split-across-callback-boundary shape. It cannot implement
		// find-first (flag must be set during iteration) and is a scoping
		// limitation, not a real evasion. Emit a diagnostic so the reverse test
		// keeps it absent from production.
		if enclosing != nil {
			// Collect guard idents in cb.Body with direct-return ifs.
			guardIdents := map[string]bool{}
			scanner.EachInSubtree[ast.IfStmt](cb.Body, func(ifStmt *ast.IfStmt) {
				if !ifBodyHasDirectReturn(ifStmt) {
					return
				}
				for name := range condIdentNames(ifStmt.Cond) {
					if falseInit[name] {
						guardIdents[name] = true
					}
				}
			})
			// Collect idents assigned `true` outside cb.Body in enclosing scope.
			assignedTrueOutside := map[string]bool{}
			scanner.EachInSubtree[ast.AssignStmt](enclosing, func(as *ast.AssignStmt) {
				// Skip assignments inside the callback itself.
				if as.Pos() >= cb.Body.Pos() && as.End() <= cb.Body.End() {
					return
				}
				if as.Tok != token.ASSIGN || len(as.Lhs) != len(as.Rhs) {
					return
				}
				for i := range as.Lhs {
					lhs, ok := as.Lhs[i].(*ast.Ident)
					if !ok {
						continue
					}
					if rhs, isID := as.Rhs[i].(*ast.Ident); isID && rhs.Name == "true" {
						assignedTrueOutside[lhs.Name] = true
					}
				}
			})
			for name := range guardIdents {
				if assignedTrueOutside[name] && !assignedTrue[name] {
					// Find the IfStmt line to report.
					scanner.EachInSubtree[ast.IfStmt](cb.Body, func(ifStmt *ast.IfStmt) {
						if !ifBodyHasDirectReturn(ifStmt) {
							return
						}
						if condIdentNames(ifStmt.Cond)[name] {
							out = append(out, scanner.Diagnostic{
								Rel: rel, Line: fset.Position(ifStmt.Pos()).Line,
								Message: "BS3 blind-spot shape (guard ident \"" + name + "\" is used " +
									"as if-return guard inside callback but `= true` is outside " +
									"the callback — non-functional find-first, scoping limitation) " +
									"in scanner/archtest.EachInChildren callback",
							})
						}
					})
				}
			}
		}

		scanner.EachInSubtree[ast.IfStmt](cb.Body, func(ifStmt *ast.IfStmt) {
			conds := condIdentNames(ifStmt.Cond)
			// BS2: the return guard is in the ELSE branch (the main detector
			// only inspects ifStmt.Body), keyed on a literal-true sentinel.
			if blk, ok := ifStmt.Else.(*ast.BlockStmt); ok {
				if _, hasRet := scanner.FindFirstChild[ast.ReturnStmt](blk, func(*ast.ReturnStmt) bool { return true }); hasRet {
					for name := range conds {
						if assignedTrue[name] {
							out = append(out, scanner.Diagnostic{
								Rel: rel, Line: fset.Position(ifStmt.Pos()).Line,
								Message: "BS2 blind-spot shape (else-branch return " +
									"guard on a false-init sentinel) in scanner/archtest.EachInChildren callback",
							})
						}
					}
				}
			}
			// BS1: a false-init sentinel used as a direct-return guard but
			// set via a non-literal-true RHS (so the main detector, which
			// keys on `= true`, misses it).
			if !ifBodyHasDirectReturn(ifStmt) {
				return
			}
			for name := range conds {
				if falseInit[name] && assignedNonLiteral[name] && !assignedTrue[name] {
					out = append(out, scanner.Diagnostic{
						Rel: rel, Line: fset.Position(ifStmt.Pos()).Line,
						Message: "BS1 blind-spot shape (false-init sentinel set via " +
							"non-true-literal RHS) in scanner/archtest.EachInChildren callback",
					})
				}
			}
		})
	})
	return out
}

// enclosingFuncBody returns the *ast.BlockStmt of the function (FuncDecl or
// FuncLit) whose body lexically contains call, or nil. Used by
// closureDoneSentinelBlindSpots to scope falseInit collection to statements
// declared before the call (F3: per-call scoping prevents cross-call
// pollution) and to search for BS3 assignments outside the callback.
func enclosingFuncBody(file *ast.File, call *ast.CallExpr) ast.Node {
	var best *ast.BlockStmt
	bestSpan := int(^uint(0) >> 1)
	consider := func(body *ast.BlockStmt) {
		if body == nil || call.Pos() < body.Pos() || call.End() > body.End() {
			return
		}
		if span := int(body.End() - body.Pos()); span < bestSpan {
			best, bestSpan = body, span
		}
	}
	scanner.EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) { consider(fd.Body) })
	scanner.EachInSubtree[ast.FuncLit](file, func(fl *ast.FuncLit) { consider(fl.Body) })
	if best == nil {
		return nil
	}
	return best
}

// TestScannerFrameworkUsage02_BlindSpotReverse asserts the documented BS1/BS2/BS3
// shapes do not exist in production archtest code. ai-collab.md requires a
// reverse self-test per declared tool blind spot; this is the 举证 material that
// the Medium rating is honest (the blind spots are empty in practice, not merely
// unguarded). BS3 is actively detected by closureDoneSentinelBlindSpots: a
// guard ident inside the callback whose `= true` assignment is outside the
// callback is a non-functional shape (cannot implement find-first) and is
// reported as a BS3 diagnostic here.
func TestScannerFrameworkUsage02_BlindSpotReverse(t *testing.T) {
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, true, nil, "./tools/archtest/...")
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}
	var diags []scanner.Diagnostic
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			if filepath.ToSlash(filepath.Dir(rel)) != "tools/archtest" {
				continue
			}
			if !strings.HasSuffix(rel, "_test.go") {
				continue
			}
			diags = append(diags, closureDoneSentinelBlindSpots(pkg.TypesInfo, pkg.Fset, file, rel)...)
		}
	}
	if len(diags) != 0 {
		t.Errorf("SCANNER-FRAMEWORK-USAGE-02 blind-spot shape present in "+
			"production (rule would silently miss it); migrate to "+
			"scanner.FindFirstChild and/or extend the detector: %v", diags)
	}
}

// TestScannerFrameworkUsage02_BlindSpotForwardFixtures documents that the
// BS1/BS2/BS3 shapes are, by design, NOT caught by forbiddenClosureDoneSentinel
// (the honest blind-spot declaration — the reverse test above is what keeps
// them empty in production).
//
// BS3 note: the MAIN detector (forbiddenClosureDoneSentinel) returns 0 hits
// because the `= true` assignment is outside the callback body, so the main
// detector's (a) pass (assignedTrue) finds nothing inside the callback and
// short-circuits. The reverse test (closureDoneSentinelBlindSpots) DOES detect
// the BS3 shape and would go RED if it appeared in production.
func TestScannerFrameworkUsage02_BlindSpotForwardFixtures(t *testing.T) {
	t.Parallel()
	// BS1/BS2/BS3 are documented blind spots of the MAIN detector
	// (forbiddenClosureDoneSentinel) — fixtures live at
	// tools/archtest/internal/usage02fixtures/bs{1,2,3}_*.go and are loaded
	// through the same typed pipeline the live scan uses.
	//
	// The reverse detector (closureDoneSentinelBlindSpots) actively catches
	// these shapes and is asserted empty in production by
	// TestScannerFrameworkUsage02_BlindSpotReverse — that test is the 举证
	// material for the Medium-on-Go-ceiling rating.
	cases := []string{
		"bs1_scanner_nonliteral_rhs",
		"bs2_scanner_else_guard",
		"bs3_scanner_assign_outside",
	}
	for _, name := range cases {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if d := loadFixture02(t, name, forbiddenClosureDoneSentinel); len(d) != 0 {
				t.Errorf("%s expected to be a (documented) blind spot of the main "+
					"detector, got %d hits: %v", name, len(d), d)
			}
		})
	}
}
