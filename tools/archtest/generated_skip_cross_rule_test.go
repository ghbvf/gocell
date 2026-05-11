package archtest

// INVARIANT: GENERATED-SKIP-CROSS-RULE-INVARIANT-01
//
// Cross-rule invariant for archtest authors. When a test loads the real
// module root via typeseval.SharedResolver / typeseval.LoadPackages with the
// "./..." pattern, the loaded package set includes generated/ packages
// (anchored by TestOutboxHandleResultFactoryPreferred_GeneratedLoadAnchor_Wave3
// in outbox_invariants_test.go). The test then iterates pkg.Syntax and runs
// AST assertions, which would silently extend the rule's scope into codegen
// output unless the test explicitly skips generated/-rel paths via
// typeseval.IsGeneratedRelPath.
//
// This invariant fires when the same file:
//
//   1. Calls typeseval.SharedResolver(...) or typeseval.LoadPackages(...)
//      with the "./..." string literal in one of its positional args, AND
//   2. The call's first positional arg ultimately resolves to findModuleRoot
//      — either an inline findModuleRoot(...) call OR an Ident whose name
//      is bound by a FILE-LEVEL AssignStmt to findModuleRoot(...) anywhere
//      in the same file (so helpers like loadModule(t, root) are caught
//      via the caller's `root := findModuleRoot(t)` assignment), AND
//   3. The file does not call typeseval.IsGeneratedRelPath anywhere.
//
// Fixture-bound helpers (prod_duration_fixtures_test.go,
// prod_clock_injection_fixtures_test.go, exported_error_new_fixtures_test.go,
// test_time_literal_fixtures_test.go, the fixture-scan helpers in
// clock_invariants_test.go and goose_session_locker_fixtures_test.go) take
// fixtureDir as a parameter and the file only ever binds fixtureDir via
// filepath.Join(...), never findModuleRoot. Criterion 2 fails by AST shape,
// with no string allowlist. Tests that load a subtree pattern (e.g.
// "./tools/archtest/internal/wrapfixture/violation" for fixture detection)
// also escape via criterion 1: the "./..." literal is absent.
//
// AI-rebust: Medium+. Three-condition AST conjunction with no string-
// anchored allowlist. True Hard upgrade (typed-wrapped resolver baking in
// skip) touches 20+ callers and is tracked separately as
// LoadProductionPackagesAllPattern in the rollout plan §3.3 "Out of scope".
//
// ref: tools/archtest/archtest_verify_coverage_test.go (canonical cross-rule
// meta-archtest model)

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func TestGeneratedSkipCrossRuleInvariant01(t *testing.T) {
	t.Parallel()
	repoRoot := findModuleRoot(t)
	scope := scanner.DirsScope(repoRoot, []string{"tools/archtest"}, scanner.IncludeTests())

	var offenders []string
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		// Restrict to the top-level archtest package, matching the script's
		// ./tools/archtest non-recursive package selector (subpackages have
		// their own ./tools/archtest/internal/... test entry and do not load
		// production packages via SharedResolver).
		if filepath.ToSlash(filepath.Dir(fc.Rel)) != "tools/archtest" {
			return
		}
		if !strings.HasSuffix(fc.Rel, "_test.go") {
			return
		}
		// Skip the meta-archtest itself so additions to the rule's body
		// (e.g. helper rename) do not trigger the invariant.
		if filepath.Base(fc.Rel) == "generated_skip_cross_rule_test.go" {
			return
		}
		if !fileLoadsRealRootAllPattern(fc.File) {
			return
		}
		if fileCallsIsGeneratedRelPath(fc.File) {
			return
		}
		offenders = append(offenders, filepath.ToSlash(fc.Rel))
	})

	sort.Strings(offenders)
	for _, rel := range offenders {
		t.Errorf("GENERATED-SKIP-CROSS-RULE-INVARIANT-01: %s loads the module root with \"./...\" "+
			"(typeseval.SharedResolver / typeseval.LoadPackages) but does not call "+
			"typeseval.IsGeneratedRelPath to skip codegen output before iterating pkg.Syntax. "+
			"Add `if typeseval.IsGeneratedRelPath(rel) { continue }` at the iteration site, "+
			"or restrict the load pattern to a subtree that excludes generated/.",
			rel)
	}
}

// fileLoadsRealRootAllPattern returns true when f contains a
// typeseval.SharedResolver / typeseval.LoadPackages call site whose
// positional args include the "./..." literal AND whose first arg resolves
// to findModuleRoot at the file level — either an inline findModuleRoot(...)
// call OR an Ident whose name is bound elsewhere in the same file by an
// AssignStmt whose RHS calls findModuleRoot. The file-level scope catches
// helper signatures like loadModule(t, root) where the param `root` is bound
// from findModuleRoot in caller test functions in the same file.
func fileLoadsRealRootAllPattern(f *ast.File) bool {
	var matched bool
	scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if matched {
			return
		}
		if !isSharedResolverOrLoadPackagesCall(call) {
			return
		}
		if len(call.Args) < 2 {
			return
		}
		if !callHasAllPatternLiteralArg(call) {
			return
		}
		if firstArgResolvesToFindModuleRoot(f, call.Args[0]) {
			matched = true
		}
	})
	return matched
}

// isSharedResolverOrLoadPackagesCall returns true when call's Fun is a
// SelectorExpr whose Sel name is SharedResolver or LoadPackages. The
// receiver package is not checked: a same-name shadow in archtest test
// code would still match here, but the rule remains fail-closed in that
// direction — a shadow file is flagged unless it also calls
// typeseval.IsGeneratedRelPath, which forces the author to import the
// real helper and surface the shadow.
func isSharedResolverOrLoadPackagesCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	switch sel.Sel.Name {
	case "SharedResolver", "LoadPackages":
		return true
	}
	return false
}

// callHasAllPatternLiteralArg returns true when the CallExpr subtree
// contains the BasicLit "./..." in any positional arg (including nested
// composite-literal slices spread via `[]string{"./..."}...`).
//
// Known limitation: the literal must appear inside the call's AST subtree.
// A file-level variable like `var pat = "./..."` referenced as
// `SharedResolver(root, false, nil, pat)` would not be caught here. AI
// authors copy-pasting the established `SharedResolver(root, ..., "./...")`
// shape will hit the inline form; the variable-indirection form is an
// uncommon shape and is left as a known Soft escape (would be Hard-closed
// by the typed-wrapped resolver tracked in the rollout plan §3.3 "Out of
// scope").
func callHasAllPatternLiteralArg(call *ast.CallExpr) bool {
	var found bool
	scanner.EachInSubtree[ast.BasicLit](call, func(lit *ast.BasicLit) {
		if !found && lit.Value == `"./..."` {
			found = true
		}
	})
	return found
}

// firstArgResolvesToFindModuleRoot returns true when arg ultimately
// represents the real repo root: an inline findModuleRoot(...) call OR an
// Ident bound by a file-level AssignStmt to findModuleRoot.
func firstArgResolvesToFindModuleRoot(f *ast.File, arg ast.Expr) bool {
	if exprIsCallToFindModuleRoot(arg) {
		return true
	}
	id := exprToIdent(arg)
	if id == nil {
		return false
	}
	return fileBindsIdentFromFindModuleRoot(f, id.Name)
}

// exprIsCallToFindModuleRoot returns true when e is a CallExpr whose Fun
// (or Fun.Sel for selector forms) is the identifier findModuleRoot.
func exprIsCallToFindModuleRoot(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name == "findModuleRoot"
	case *ast.SelectorExpr:
		return fun.Sel != nil && fun.Sel.Name == "findModuleRoot"
	}
	return false
}

// fileBindsIdentFromFindModuleRoot returns true when f contains any
// AssignStmt whose LHS contains an Ident named name AND whose RHS contains
// a call to findModuleRoot. File-level scope so a helper's parameter (e.g.
// `root` in loadModule(t, root)) is matched against its callers'
// `root := findModuleRoot(t)` assignments in the same file.
func fileBindsIdentFromFindModuleRoot(f *ast.File, name string) bool {
	var matched bool
	scanner.EachInSubtree[ast.AssignStmt](f, func(as *ast.AssignStmt) {
		if matched {
			return
		}
		if !assignLhsHasIdentNamed(as, name) {
			return
		}
		if !assignRhsCallsFindModuleRoot(as) {
			return
		}
		matched = true
	})
	return matched
}

// assignLhsHasIdentNamed returns true when as.Lhs contains an Ident whose
// Name equals name. The type assertion is funneled through exprToIdent
// (defined in security_defaults_test.go) so the for-range loop body has no
// inline type assertion — keeping the scan compliant with
// SCANNER-FRAMEWORK-USAGE-01 form (a) detection.
func assignLhsHasIdentNamed(as *ast.AssignStmt, name string) bool {
	for _, lhs := range as.Lhs {
		if id := exprToIdent(lhs); id != nil && id.Name == name {
			return true
		}
	}
	return false
}

// assignRhsCallsFindModuleRoot returns true when any expression in as.Rhs is
// a call to findModuleRoot (the only form callers use to discover the real
// repo go.mod ancestor in archtest tests).
func assignRhsCallsFindModuleRoot(as *ast.AssignStmt) bool {
	for _, rhs := range as.Rhs {
		if exprIsCallToFindModuleRoot(rhs) {
			return true
		}
	}
	return false
}

// fileCallsIsGeneratedRelPath returns true when f references
// typeseval.IsGeneratedRelPath via a SelectorExpr whose receiver matches
// the file's import alias for the typeseval package. Verifying the import
// closes the AI-shadowing escape (declaring a local
// `func (l *localChecker) IsGeneratedRelPath(...) bool` or dot-importing
// a same-named symbol from an unrelated package), which would otherwise
// degrade the criterion to a Soft string anchor on the bare method name.
//
// If the file does not import typeseval at all, no call site can satisfy
// the invariant — the file is treated as non-compliant (fail-closed).
// Dot-import (`import . "...typeseval"`) is not supported here: it would
// require the bare-Ident form which is indistinguishable from a local
// helper at the AST level without type information. Archtest tests use
// the standard named-import form (no dot-imports of typeseval anywhere in
// the repo), so this is a controlled restriction.
func fileCallsIsGeneratedRelPath(f *ast.File) bool {
	alias, ok := typesevalImportAlias(f)
	if !ok {
		return false
	}
	var found bool
	scanner.EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
		if found || sel.Sel == nil || sel.Sel.Name != "IsGeneratedRelPath" {
			return
		}
		recv := exprToIdent(sel.X)
		if recv != nil && recv.Name == alias {
			found = true
		}
	})
	return found
}

// typesevalImportAlias returns the local name under which f imports the
// typeseval package, plus a found flag. Default named imports use the
// package name ("typeseval"); explicit aliases use the import-spec Name.
// Dot-imports and underscore-imports are reported as not-found because the
// invariant's selector form cannot resolve through them.
func typesevalImportAlias(f *ast.File) (string, bool) {
	const path = `"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"`
	for _, imp := range f.Imports {
		if imp.Path == nil || imp.Path.Value != path {
			continue
		}
		if imp.Name == nil {
			return "typeseval", true
		}
		switch imp.Name.Name {
		case "_", ".":
			return "", false
		}
		return imp.Name.Name, true
	}
	return "", false
}

// TestGeneratedSkipCrossRuleInvariant01_DetectionCapability anchors the
// detection logic of fileLoadsRealRootAllPattern + fileCallsIsGeneratedRelPath
// against a battery of inline AST fixtures. If a future refactor of the
// underlying scanner.EachIn* primitives or AST node-kind selection silently
// breaks detection, this test would report 0/N catches instead of N/N — a
// regression the real-repo TestGeneratedSkipCrossRuleInvariant01 above
// cannot surface because it only ever runs against a (presumably compliant)
// post-fix tree.
//
// Per ai-collab.md §"real source AST capture": fixtures are real Go source
// parsed via go/parser; hand-crafted ast.File literals are not used.
func TestGeneratedSkipCrossRuleInvariant01_DetectionCapability(t *testing.T) {
	t.Parallel()
	const typesevalImport = `"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"`

	cases := []struct {
		name           string
		src            string
		wantLoadAllPat bool
		wantSkipCall   bool
	}{
		{
			name: "violation_direct_root_literal_pattern_no_skip",
			src: `package fixture
import (
	"testing"
	typeseval ` + typesevalImport + `
)
func findModuleRoot(t *testing.T) string { return "" }
func TestViolation(t *testing.T) {
	root := findModuleRoot(t)
	_, _ = typeseval.SharedResolver(root, false, nil, "./...")
}`,
			wantLoadAllPat: true,
			wantSkipCall:   false,
		},
		{
			name: "compliant_explicit_skip_call",
			src: `package fixture
import (
	"testing"
	typeseval ` + typesevalImport + `
)
func findModuleRoot(t *testing.T) string { return "" }
func TestCompliant(t *testing.T) {
	root := findModuleRoot(t)
	_, _ = typeseval.SharedResolver(root, false, nil, "./...")
	_ = typeseval.IsGeneratedRelPath("generated/foo")
}`,
			wantLoadAllPat: true,
			wantSkipCall:   true,
		},
		{
			name: "shadow_local_method_does_not_satisfy_skip",
			src: `package fixture
import (
	"testing"
	typeseval ` + typesevalImport + `
)
func findModuleRoot(t *testing.T) string { return "" }
type localChecker struct{}
func (c localChecker) IsGeneratedRelPath(rel string) bool { return false }
func TestShadow(t *testing.T) {
	root := findModuleRoot(t)
	_, _ = typeseval.SharedResolver(root, false, nil, "./...")
	_ = (localChecker{}).IsGeneratedRelPath("generated/foo")
}`,
			wantLoadAllPat: true,
			wantSkipCall:   false,
		},
		{
			name: "fixture_caller_excluded_by_structure",
			src: `package fixture
import (
	"path/filepath"
	"testing"
	typeseval ` + typesevalImport + `
)
func findModuleRoot(t *testing.T) string { return "" }
func TestFixtureLoader(t *testing.T) {
	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "testdata")
	_, _, _ = typeseval.LoadPackages(fixtureDir, false, nil, "./...")
}`,
			wantLoadAllPat: false,
			wantSkipCall:   false,
		},
		{
			name: "subset_pattern_not_matched",
			src: `package fixture
import (
	"testing"
	typeseval ` + typesevalImport + `
)
func findModuleRoot(t *testing.T) string { return "" }
func TestSubset(t *testing.T) {
	root := findModuleRoot(t)
	_, _ = typeseval.SharedResolver(root, false, nil, "./runtime/...")
}`,
			wantLoadAllPat: false,
			wantSkipCall:   false,
		},
		{
			name: "helper_signature_root_param_caught_via_caller_assignment",
			src: `package fixture
import (
	"testing"
	typeseval ` + typesevalImport + `
)
func findModuleRoot(t *testing.T) string { return "" }
func loadHelper(t *testing.T, root string) {
	_, _ = typeseval.SharedResolver(root, false, nil, "./...")
}
func TestHelperCaller(t *testing.T) {
	root := findModuleRoot(t)
	loadHelper(t, root)
}`,
			wantLoadAllPat: true,
			wantSkipCall:   false,
		},
		{
			name: "no_typeseval_import_skip_unreachable",
			src: `package fixture
import "testing"
func findModuleRoot(t *testing.T) string { return "" }
func TestUnreachable(t *testing.T) {
	root := findModuleRoot(t)
	_ = root
}`,
			wantLoadAllPat: false,
			wantSkipCall:   false,
		},
		{
			name: "load_pattern_in_composite_literal_subtree",
			src: `package fixture
import (
	"testing"
	typeseval ` + typesevalImport + `
)
func findModuleRoot(t *testing.T) string { return "" }
func TestCompositeLit(t *testing.T) {
	root := findModuleRoot(t)
	_, _, _ = typeseval.LoadPackages(root, false, nil, []string{"./..."}...)
}`,
			wantLoadAllPat: true,
			wantSkipCall:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, tc.name+".go", tc.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			gotLoadAllPat := fileLoadsRealRootAllPattern(f)
			gotSkipCall := fileCallsIsGeneratedRelPath(f)
			if gotLoadAllPat != tc.wantLoadAllPat {
				t.Errorf("fileLoadsRealRootAllPattern = %v, want %v", gotLoadAllPat, tc.wantLoadAllPat)
			}
			if gotSkipCall != tc.wantSkipCall {
				t.Errorf("fileCallsIsGeneratedRelPath = %v, want %v", gotSkipCall, tc.wantSkipCall)
			}
		})
	}
}
