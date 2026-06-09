package archtest

// auth_plan.go — importable AUTH-PLAN rule logic (#1632 M3).
//
// This is the non-test home of the AUTH-PLAN-01/02/03/04 scanner logic so they
// can be compiled and run by an external Cell repository (Go never compiles a
// dependency's _test.go, so rule logic external repos must run cannot live in a
// _test.go file). GoCell's own Test* functions in auth_plan_test.go call the
// same Check* — single source, no parallel rule body.
//
// Four rules prevent regression to the old string-based cell.Policy dispatch:
//
//   AUTH-PLAN-01  No literal strings "jwt", "mtls", "service-token", "stack["
//                 in production .go files (these were the Policy.Name values).
//   AUTH-PLAN-02  No selector expression bootstrap.Policy{None,JWT,…} —
//                 all seven legacy factory names are deleted.
//   AUTH-PLAN-03  No cell.Policy composite literal or type reference (deleted type).
//   AUTH-PLAN-04  LAYER-09: cells/ code must not construct AuthPlan structs
//                 (AuthJWT, AuthJWTFromAssembly, AuthMTLS, etc.) — composition
//                 root responsibility only.
//
// Implements rules AUTH-PLAN-01 / -02 / -03 / -04. The canonical `// INVARIANT:`
// anchors live in auth_plan_test.go (the inventory scan reads only _test.go);
// they are intentionally not repeated here as markers to avoid a second source.
//
// ref: kubernetes/apiserver pkg/authentication/authenticator/interfaces.go@master
//      — typed authenticator interface, no string-keyed dispatch.
// ref: go-kratos/kratos transport/http/server.go@main
//      — middleware assembled at composition root, not inside application code.
//
// Not registered in StandardCellRules: AUTH-PLAN-01/02/03 use module-root AST
// scanning and AUTH-PLAN-04 allowlists are gocell-hardcoded — would be vacuous
// or false-red for an external cell; kept importable & module-path-agnostic but
// not consumer-portable.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// ---------------------------------------------------------------------------
// Rule constants
// ---------------------------------------------------------------------------

const (
	authPlanRule01 = "AUTH-PLAN-01"
	authPlanRule02 = "AUTH-PLAN-02"
	authPlanRule03 = "AUTH-PLAN-03"
	authPlanRule04 = "AUTH-PLAN-04"
)

// forbiddenPolicyStrings are the old cell.Policy.Name values that must no
// longer appear as string literals in production code. The canonical source of
// truth is now the Describe() method of each typed plan.
var forbiddenPolicyStrings = []string{
	`"jwt"`,
	`"mtls"`,
	`"service-token"`,
	`"stack["`,
}

// forbiddenPolicySelectors are selector names that existed on the now-deleted
// bootstrap.Policy* factory functions.
var forbiddenPolicySelectors = []string{
	"PolicyNone",
	"PolicyJWT",
	"PolicyJWTFromAssembly",
	"PolicyMTLS",
	"PolicyServiceToken",
	"PolicyVerboseToken",
	"PolicyStack",
}

// authPlanConstructorNames are the AuthPlan struct type names that cells/ must
// not construct directly (LAYER-09). Composition roots (cmd/, examples/) are
// exempt.
var authPlanConstructorNames = map[string]struct{}{
	"AuthJWT":             {},
	"AuthJWTFromAssembly": {},
	"AuthMTLS":            {},
	"AuthNone":            {},
	"AuthServiceToken":    {},
	"AuthOperator":        {}, // #1505 operator control-plane listener gate
}

// authPlanPkgPath is the canonical import path of the package that owns the
// AuthPlan constructor names and types. Resolved via go/types — the package
// alias chosen at any given import site (`auth`, `kauth`, `myauth`, …) is
// irrelevant; only the import path identifies the type's origin.
const authPlanPkgPath = PlatformModulePath + "/kernel/auth"

// authPlanStringAllowlist contains files that legitimately return or use the
// old policy-name strings as Describe() return values, observability labels,
// or unrelated map keys. These are not dispatch discriminators.
//
// Rule: any file in this list may contain the strings in forbiddenPolicyStrings.
// The rule still catches new callers that add the strings outside this list.
var authPlanStringAllowlist = []string{
	// Canonical Describe() return values live in auth_plan.go.
	"kernel/auth/auth_plan.go",
	// describeAuthChain — the single file allowed to assemble describe strings.
	"runtime/bootstrap/auth_plan_describe.go",
	// Observability labels (AuthMethod="jwt") — not dispatch.
	"runtime/auth/authenticator.go",
	// Vault policy map — "jwt" is a Vault policy name key, not an auth discriminator.
	"adapters/vault/auth.go",
}

// ---------------------------------------------------------------------------
// AUTH-PLAN-01: no forbidden policy string literals
// ---------------------------------------------------------------------------

// CheckAuthPlanNoLegacyPolicyStringLiterals enforces AUTH-PLAN-01:
// the string values that were used as cell.Policy.Name discriminators
// ("jwt", "mtls", "service-token", "stack[") must not appear as bare string
// literals in production .go files outside the canonical allowlist.
//
// Allowlisted files (see authPlanStringAllowlist) may contain these strings
// because they own the canonical Describe() definitions or use them as
// observability labels rather than dispatch keys.
func CheckAuthPlanNoLegacyPolicyStringLiterals(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)

	files, err := findAllProductionGoFiles(root)
	if err != nil {
		t.Errorf("%s: findAllProductionGoFiles: %v", authPlanRule01, err)
		return nil
	}

	var diags []Diagnostic
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		if authPlanStringAllowlisted(rel) {
			continue // allowlisted files own these strings legitimately
		}
		diags = append(diags, scanForbiddenPolicyStrings(f, rel)...)
	}
	return diags
}

// authPlanStringAllowlisted reports whether rel owns the canonical policy-string
// definitions / observability labels and is therefore exempt from AUTH-PLAN-01.
func authPlanStringAllowlisted(rel string) bool {
	for _, allowed := range authPlanStringAllowlist {
		if strings.HasSuffix(rel, allowed) {
			return true
		}
	}
	return false
}

// scanForbiddenPolicyStrings parses one production file and returns a diagnostic
// for every bare forbidden policy string literal (AUTH-PLAN-01).
func scanForbiddenPolicyStrings(f, rel string) []Diagnostic {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil // unparseable; other tools will catch syntax errors
	}
	var diags []Diagnostic
	scanner.EachInSubtree[ast.BasicLit](af, func(bl *ast.BasicLit) {
		if bl.Kind != token.STRING {
			return
		}
		for _, forbidden := range forbiddenPolicyStrings {
			if bl.Value != forbidden { // bl.Value is the raw quoted string, e.g. `"jwt"`
				continue
			}
			line := fset.Position(bl.Pos()).Line
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf("%s: %s:%d: forbidden policy string literal %s",
					authPlanRule01, rel, line, bl.Value),
			})
		}
	})
	return diags
}

// ---------------------------------------------------------------------------
// AUTH-PLAN-02: no deleted bootstrap.Policy* selector expressions
// ---------------------------------------------------------------------------

// CheckAuthPlanNoLegacyPolicySelectorExpressions enforces AUTH-PLAN-02:
// the seven deleted bootstrap.Policy* factory functions must not be referenced
// anywhere in the codebase. This catches accidental re-introduction of the
// old API.
func CheckAuthPlanNoLegacyPolicySelectorExpressions(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)

	files, err := findAllProductionGoFiles(root)
	if err != nil {
		t.Errorf("%s: findAllProductionGoFiles: %v", authPlanRule02, err)
		return nil
	}

	var diags []Diagnostic
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		diags = append(diags, scanForbiddenPolicySelectors(f, rel)...)
	}
	return diags
}

// scanForbiddenPolicySelectors parses one production file and returns a
// diagnostic for every reference to a deleted bootstrap.Policy* selector
// (AUTH-PLAN-02).
func scanForbiddenPolicySelectors(f, rel string) []Diagnostic {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var diags []Diagnostic
	scanner.EachInSubtree[ast.SelectorExpr](af, func(sel *ast.SelectorExpr) {
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != "bootstrap" {
			return
		}
		for _, forbidden := range forbiddenPolicySelectors {
			if sel.Sel.Name != forbidden {
				continue
			}
			line := fset.Position(sel.Pos()).Line
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf("%s: %s:%d: forbidden selector %s",
					authPlanRule02, rel, line, "bootstrap."+forbidden),
			})
		}
	})
	return diags
}

// ---------------------------------------------------------------------------
// AUTH-PLAN-03: no cell.Policy composite literals or type references
// ---------------------------------------------------------------------------

// CheckAuthPlanNoCellPolicyTypeUsage enforces AUTH-PLAN-03:
// cell.Policy is a deleted type. Neither `cell.Policy{…}` composite literals
// nor `cell.Policy` identifier references should appear in any .go file.
func CheckAuthPlanNoCellPolicyTypeUsage(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)

	files, err := findAllProductionGoFiles(root)
	if err != nil {
		t.Errorf("%s: findAllProductionGoFiles: %v", authPlanRule03, err)
		return nil
	}

	var diags []Diagnostic
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		diags = append(diags, scanCellPolicyUsage(f, rel)...)
	}
	// De-duplicate (a composite lit also matches the selector inside its type).
	return dedupAuthPlanDiags(diags)
}

// scanCellPolicyUsage parses one production file and returns a diagnostic for
// every cell.Policy composite literal or type reference (AUTH-PLAN-03).
func scanCellPolicyUsage(f, rel string) []Diagnostic {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	mk := func(line int, kind string) Diagnostic {
		return Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf("%s: %s:%d: cell.Policy %s (type was deleted in PR262)",
				authPlanRule03, rel, line, kind),
		}
	}
	var diags []Diagnostic
	scanner.EachInSubtree[ast.CompositeLit](af, func(lit *ast.CompositeLit) {
		if sel, ok := lit.Type.(*ast.SelectorExpr); ok && isCellPolicySelector(sel) {
			diags = append(diags, mk(fset.Position(lit.Pos()).Line, "composite literal"))
		}
	})
	scanner.EachInSubtree[ast.SelectorExpr](af, func(sel *ast.SelectorExpr) {
		if isCellPolicySelector(sel) {
			diags = append(diags, mk(fset.Position(sel.Pos()).Line, "type reference"))
		}
	})
	return diags
}

// isCellPolicySelector reports whether sel is the deleted `cell.Policy` selector.
func isCellPolicySelector(sel *ast.SelectorExpr) bool {
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "cell" && sel.Sel.Name == "Policy"
}

// dedupAuthPlanDiags removes duplicate diagnostics keyed by (Rel, Line, Message).
func dedupAuthPlanDiags(diags []Diagnostic) []Diagnostic {
	seen := make(map[string]bool, len(diags))
	out := diags[:0]
	for _, d := range diags {
		key := fmt.Sprintf("%s:%d:%s", d.Rel, d.Line, d.Message)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	return out
}

// ---------------------------------------------------------------------------
// AUTH-PLAN-04 (LAYER-09): cells/ must not construct AuthPlan values
// ---------------------------------------------------------------------------

// CheckAuthPlanCellsMustNotConstructAuthPlans enforces AUTH-PLAN-04 (LAYER-09):
// AuthPlan values (AuthJWT, AuthJWTFromAssembly, AuthMTLS, etc.) are
// composition-root concerns and must only be constructed in cmd/ and
// examples/. Cells and runtime (except runtime/bootstrap/ which is the wiring
// layer) must not instantiate the concrete types — that would couple business
// logic to listener topology decisions.
//
// Scanned packages:
//   - all production packages under cells/ and runtime/ (the Production scope
//     already excludes generated/)
//   - runtime/bootstrap/... is the authorized wiring layer and is skipped
//
// Resolution model — typed (Hard upgrade, PR #615 review fix):
//
// Both branches resolve the symbol's defining package via go/types
// (*types.Info.Uses / TypeOf), NOT via the AST package-qualifier identifier:
//
//   - Constructor calls (`foo.NewAuthJWT(...)`): Uses[sel.Sel] returns a
//     *types.Func; we check fn.Pkg().Path() == authPlanPkgPath and
//     fn.Name() in {NewAuth*}.
//
//   - Composite literals (`foo.AuthMTLS{}`, `AuthMTLS{}` inside the auth
//     package itself): we walk the type-AST (SelectorExpr or Ident) to its
//     *ast.Ident, look up Uses[ident] to get a *types.TypeName, and check
//     tn.Pkg().Path() == authPlanPkgPath and tn.Name() in
//     authPlanConstructorNames.
//
// Why typed: parser-only + import-alias allowlist (the previous form) went
// vacuously empty after the kernel/cell → kernel/auth move because the check
// was `pkg.Name == "cell"`. Maintaining an explicit alias allow-list ({auth,
// kauth, …}) makes any new alias a silent bypass — the AI-robust gap the
// reviewer flagged. Resolving through go/types is alias-agnostic by
// construction: `import myauth "github.com/ghbvf/gocell/kernel/auth"` still
// resolves Uses[*ast.Ident{Name:"myauth"}] back to the kernel/auth package
// object whose Path() is authPlanPkgPath.
//
// Blind spots (documented per ai-robust.md "工具选定后强制盲区自检"):
//
//   - reflective construction via reflect.New(reflect.TypeOf(auth.AuthMTLS{}))
//     — the typed walk catches the auth.AuthMTLS{} composite literal, so this
//     form is covered transitively.
//   - method values / function pointers (e.g. `f := auth.NewAuthJWT`) — the
//     Uses[*ast.Ident] resolution still fires on the SelectorExpr.Sel of the
//     RHS, so the rule still flags the assignment.
//   - dot-import (`import . "github.com/ghbvf/gocell/kernel/auth"` then
//     `_ = AuthMTLS{}`) — the type-AST is then a bare *ast.Ident, and
//     Uses[ident] still resolves to the kernel/auth *types.TypeName. Covered.
//   - cgo / unsafe-pointer construction — not expressible for sealed
//     interface implementations like AuthPlan. Out of scope.
func CheckAuthPlanCellsMustNotConstructAuthPlans(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Errorf("%s: read module path from go.mod: %v", authPlanRule04, err)
		return nil
	}

	// cfg is unused: scan scope is the fixed Production set; not consumer-portable.
	return Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		relPkg := strings.TrimPrefix(p.Pkg.Path(), modPath+"/")
		if !isAuthPlanScannedPkg(relPkg) {
			return nil
		}
		origin := authPlanPkgOrigin(relPkg)

		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			out = append(out, authPlanCompositeViolations(p, file, rel, origin)...)
			out = append(out, authPlanConstructorCallViolations(p, file, rel, origin)...)
		}
		return out
	})
}

// authPlanCompositeViolations flags `auth.AuthXxx{}` composite-literal
// construction of AuthPlan types in a scanned file (AUTH-PLAN-04 / LAYER-09).
// Resolution is typed (resolveTypeNameForComposite), so import aliases and
// dot-imports resolve to the same kernel/auth *types.TypeName.
func authPlanCompositeViolations(p *Pass, file *ast.File, rel, origin string) []Diagnostic {
	var out []Diagnostic
	scanner.EachInSubtree[ast.CompositeLit](file, func(node *ast.CompositeLit) {
		tn := resolveTypeNameForComposite(p.TypesInfo, node.Type)
		if tn == nil || tn.Pkg() == nil || tn.Pkg().Path() != authPlanPkgPath {
			return
		}
		if _, forbidden := authPlanConstructorNames[tn.Name()]; !forbidden {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(node.Pos()).Line,
			Message: fmt.Sprintf("%s: %s constructs %s via composite literal (LAYER-09 violation)",
				authPlanRule04, origin, tn.Name()),
		})
	})
	return out
}

// authPlanConstructorCallViolations flags `auth.NewAuthXxx(...)` constructor
// calls in a scanned file (AUTH-PLAN-04 / LAYER-09). Resolution is typed
// (Uses[sel.Sel].(*types.Func)), so the defining package is alias-agnostic.
func authPlanConstructorCallViolations(p *Pass, file *ast.File, rel, origin string) []Diagnostic {
	var out []Diagnostic
	scanner.EachInSubtree[ast.CallExpr](file, func(node *ast.CallExpr) {
		sel, ok := node.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		fn, ok := p.TypesInfo.Uses[sel.Sel].(*types.Func)
		if !ok || fn.Pkg() == nil || fn.Pkg().Path() != authPlanPkgPath {
			return
		}
		if !strings.HasPrefix(fn.Name(), "NewAuth") {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(node.Pos()).Line,
			Message: fmt.Sprintf("%s: %s constructs %s via constructor call (LAYER-09 violation)",
				authPlanRule04, origin, fn.Name()),
		})
	})
	return out
}

// isAuthPlanScannedPkg reports whether the package at relPkg (module-relative
// slash-path, e.g. "corecells/accesscore/slices/setup") is in scope for AUTH-PLAN-04
// (i.e. under cells/ or runtime/ but not runtime/bootstrap/).
func isAuthPlanScannedPkg(relPkg string) bool {
	if strings.HasPrefix(relPkg, "runtime/bootstrap") {
		return false
	}
	return strings.HasPrefix(relPkg, "cells/") || strings.HasPrefix(relPkg, "runtime/")
}

// authPlanPkgOrigin returns the human-readable origin label used in
// AUTH-PLAN-04 diagnostics ("cells/" or "runtime/ (non-bootstrap)").
func authPlanPkgOrigin(relPkg string) string {
	if strings.HasPrefix(relPkg, "cells/") {
		return "cells/"
	}
	return "runtime/ (non-bootstrap)"
}

// resolveTypeNameForComposite walks the type-AST of a CompositeLit and returns
// the *types.TypeName it resolves to (or nil for built-in container literals
// like []T{}, map[K]V{}, struct literals with anonymous type). Handles both
// the qualified form (`pkg.Foo{}`) and the bare form (`Foo{}` — inside the
// owning package or under a dot-import).
func resolveTypeNameForComposite(info *types.Info, typ ast.Expr) *types.TypeName {
	if info == nil || typ == nil {
		return nil
	}
	var ident *ast.Ident
	switch t := typ.(type) {
	case *ast.SelectorExpr:
		ident = t.Sel
	case *ast.Ident:
		ident = t
	default:
		return nil
	}
	tn, _ := info.Uses[ident].(*types.TypeName)
	return tn
}

// ---------------------------------------------------------------------------
// File-finding helpers
// ---------------------------------------------------------------------------

// findAllProductionGoFiles returns all non-test .go files under root,
// excluding vendor/, .git/, generated/, testdata/, worktrees/ directories and *_test.go.
func findAllProductionGoFiles(root string) ([]string, error) {
	return scanner.ModuleScope(root).Files()
}

// findProductionGoFilesInDir returns production .go files under a specific dir.
// dir must be an absolute path under root (module root).
func findProductionGoFilesInDir(dir string) ([]string, error) {
	root := moduleRootOf(dir)
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return nil, err
	}
	return scanner.DirsScope(root, []string{filepath.ToSlash(rel)}).Files()
}

// moduleRootOf walks up from dir to find the nearest go.mod file and returns
// the directory containing it. This avoids threading root through every caller.
func moduleRootOf(dir string) string {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := filepath.EvalSymlinks(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			panic("moduleRootOf: go.mod not found above " + dir)
		}
	}
}
