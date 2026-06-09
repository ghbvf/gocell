package archtest

// no_deleted_auth_symbols.go — importable NO-DELETED-AUTH-SYMBOLS-01 rule
// logic (#1632 M3).
//
// This is the non-test home of the NO-DELETED-AUTH-SYMBOLS-01 scanner so
// it can be compiled and run by an external Cell repository (Go never compiles a
// dependency's _test.go, so rule logic external repos must run cannot live in a
// _test.go file). GoCell's own TestNO_DELETED_AUTH_SYMBOLS_01 in
// no_deleted_auth_symbols_test.go calls the same CheckNoDeletedAuthSymbols01
// — single source, no parallel rule body.
//
// # NO-DELETED-AUTH-SYMBOLS-01
//
// Invariant: no production or test .go file (in any package, including
// runtime/auth/ itself) may reference or re-declare the deleted symbols
//   - auth.RoleInternalAdmin
//   - auth.ServiceNameInternal
//   - auth.BuiltinServiceRoles
//
// These symbols were removed in Wave 2 of the SVCTOKEN-CALLER-IDENTITY
// migration (PR #362 "A5 service token caller_cell + contract.clients
// runtime enforce"). The rule enforces a literal "0 references / 0
// re-declarations" check — re-introduction in ANY package (external,
// dot-imported, OR runtime/auth itself) fails CI. There is NO package-level
// carve-out for runtime/auth: the symbols were physically deleted, so
// re-declaration or self-use inside runtime/auth is exactly what the rule
// must catch (closing the BS-同包 gap raised in PR #1180 review).
//
// # AI-robust 评级：Medium-true (type-aware via typeseval.ResolvePackageRef)
//
// Resolution is by canonical *types.PkgName import path (info.Uses[sel.X]
// .(*types.PkgName).Imported().Path()), NOT AST identifier name, so import
// aliases (`import authz "...runtime/auth"; authz.RoleInternalAdmin`) cannot
// bypass detection and same-name decoys (`import auth "other/pkg"`) cannot
// trigger false positives. This closes the Soft-tier debt called out by
// docs/reviews/202605181109-042-archtest-six-agent-audit.md §3a 合规红线第 2
// 条; the upgrade follows ai-robust.md §Hard 范本目录 "string-typed concept
// funnel" (Medium天花板 per 042 §3b: Go 类型系统对黑名单引用守护的客观上限).
//
// # Detection
//
// Three AST forms together cover every cross-package reference AND every
// in-package re-declaration of a banned identifier:
//   - (A) qualified SelectorExpr `pkg.X` — alias-transparent via
//     info.Uses[sel.X].(*types.PkgName).Imported().Path() (covers consts +
//     funcs at any expression position, including value-capture
//     `var fn = auth.BuiltinServiceRoles`). Resolution delegates to
//     ResolvePackageRef typed façade.
//   - (B) bare *ast.Ident reference (info.Uses) — covers dot-imported
//     references (`import . "...runtime/auth"; _ = RoleInternalAdmin / _ =
//     ServiceNameInternal / BuiltinServiceRoles(...)`) AND same-package
//     self-references inside authImportPath. Uses info.Uses[id] directly
//     with type switch over {*types.Const, *types.Var, *types.Func,
//     *types.TypeName}, bypassing ResolvePackageRef's typed-callable filter
//     (which intentionally rejects Const/Var per its godoc).
//   - (C) bare *ast.Ident declaration (info.Defs) — catches package-scope
//     re-declaration inside authImportPath (`const RoleInternalAdmin =
//     "..."` / `func BuiltinServiceRoles(...)`). Only fires when the pass
//     IS authImportPath; declarations in other packages with matching
//     names are unrelated symbols. Local scope (function bodies, struct
//     fields) is excluded via obj.Parent() == obj.Pkg().Scope() check.
//
// # Blind spots
//
//   - BS-1 Reflection via reflect.Value.{Field,Method}ByName with constant
//     string argument: NOT a Go identifier, so neither (A) nor (B) sees it.
//     Reverse self-check TestNO_DELETED_AUTH_SYMBOLS_01_BS1_NoReflectAccess
//     reuses the shared scanReflectStringArgCalls (REFLECT-STRING-ARG-SCANNER-01)
//     to assert production AST has zero such references; companion
//     TestNO_DELETED_AUTH_SYMBOLS_01_BS1_FixtureCatchesReflect pins the
//     scanner-logic-verified contract via a fixture exercising both method-value
//     and method-expression call forms. Covers chained shapes like
//     `reflect.ValueOf(x).MethodByName(...)` and
//     `reflect.Value.FieldByName(recv, ...)` uniformly.
//   - BS-2 (accepted) `//go:linkname` directive: AST scanners do not parse
//     compiler directives, so `//go:linkname myLocal
//     github.com/.../runtime/auth.BuiltinServiceRoles` would not be detected
//     by either (A) or (B). The symbols are physically deleted, so a
//     linkname reference fails at link time (LINKLOAD does not resolve);
//     this is a build-time guard rather than a CI archtest gap. Sole
//     accepted residual.
//
// # Hard is unattainable for this rule shape
//
// "Deleted symbol reference 禁用" is a blacklist guard; Go's type system
// has no per-symbol reference封禁 mechanism, and the Hard 范本目录
// (sealing / single-sanctioned-holder / typed-marker / codegen-funnel) is
// oriented at whitelist / single-construction-site semantics. The current
// type-aware archtest is the Go ceiling per ai-robust.md §载体决策原则 ≥
// Medium 立项硬门槛.
//
// Not registered in StandardCellRules: allowlist is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension → would false-red an external cell's
// own auth code; kept importable & module-path-agnostic but vacuous-green
// externally.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"testing"
)

const ruleNoDeletedAuthSymbols01 = "NO-DELETED-AUTH-SYMBOLS-01"

// fixtureAuthImportPath is the canonical path of the fixture-local fake
// `auth` package used by TestNO_DELETED_AUTH_SYMBOLS_01_FixtureCatchesAllForms.
// It is intentionally distinct from authRuntimeImportPath (defined in
// svctoken_caller_cell.go) so the same scanner function exercises both
// targets without redeclaring constants in production scope.
const fixtureAuthImportPath = PlatformModulePath + "/tools/archtest/internal/nodeletedauthsymbolsfixture/auth"

// deletedAuthSymbols is the set of selector / identifier names that must not
// appear in any reference outside the canonical definition site.
var deletedAuthSymbols = map[string]bool{
	"RoleInternalAdmin":   true,
	"ServiceNameInternal": true,
	"BuiltinServiceRoles": true,
}

// scanDeletedAuthSymbolsAgainst walks a typed Pass and records every
// reference OR package-scope re-declaration of a banned identifier whose
// owning package resolves to authImportPath. Parametrized over authImportPath
// so the production invariant test (authRuntimeImportPath) and the fixture
// self-check (fixtureAuthImportPath) share one implementation.
//
// No package-level carve-out: the symbols were physically deleted in Wave 2,
// so re-introduction inside authImportPath itself is exactly what the rule
// must catch. The PR文案 "0 references / re-introduction fails CI" is
// implemented literally — any reference or package-scope re-declaration in
// any package (including authImportPath itself) is reported.
func scanDeletedAuthSymbolsAgainst(p *Pass, authImportPath string) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	selfPackage := p.Pkg != nil && p.Pkg.Path() == authImportPath
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		selSel := collectSelSelPositions(file)
		diags = append(diags, scanQualifiedBannedRefs(p, file, rel, authImportPath)...)
		diags = append(diags, scanBareBannedRefs(p, file, rel, authImportPath, selfPackage, selSel)...)
		if selfPackage {
			// (C) Package-scope re-declaration inside authImportPath — only when
			// the pass IS authImportPath; same-name declarations elsewhere are
			// unrelated symbols.
			diags = append(diags, scanBannedRedeclarations(p, file, rel, authImportPath)...)
		}
	}
	return diags
}

// collectSelSelPositions records the positions of every SelectorExpr.Sel so the
// bare-Ident scan (form B) does not double-count an Ident that is already the
// Sel half of a qualified selector caught by form A. Without this guard,
// EachInSubtree[ast.Ident] would visit `auth.RoleInternalAdmin`'s
// `RoleInternalAdmin` Sel-Ident and resolve it (via info.Uses) to the same
// *types.Const / *types.Func that (A) already recorded — every qualified
// reference would be reported twice.
func collectSelSelPositions(file *ast.File) map[token.Pos]bool {
	positions := make(map[token.Pos]bool)
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel != nil {
			positions[sel.Sel.Pos()] = true
		}
	})
	return positions
}

// scanQualifiedBannedRefs is form (A): qualified SelectorExpr `pkg.X`,
// alias-transparent via ResolvePackageRef.
func scanQualifiedBannedRefs(p *Pass, file *ast.File, rel, authImportPath string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, sel)
		if !ok || pkgPath != authImportPath || !deletedAuthSymbols[name] {
			return
		}
		diags = append(diags, Diagnostic{
			Rel:     rel,
			Line:    p.Fset.Position(sel.Pos()).Line,
			Message: formatBannedSymbolDiag(authImportPath, name, ""),
		})
	})
	return diags
}

// scanBareBannedRefs is form (B): bare *ast.Ident reference (Const / Var / Func
// / TypeName). Uses info.Uses[id] directly so both dot-imported bare refs AND
// same-package self-references inside authImportPath are detected
// (ResolvePackageRef intentionally rejects Const/Var per its typed-callable
// filter; that limitation is bypassed here, keeping the façade unchanged —
// façade extension is tracked separately by #1037). selSel skips Idents already
// recorded by form (A).
func scanBareBannedRefs(p *Pass, file *ast.File, rel, authImportPath string, selfPackage bool, selSel map[token.Pos]bool) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
		if selSel[id.Pos()] || !deletedAuthSymbols[id.Name] {
			return
		}
		obj := p.TypesInfo.Uses[id]
		if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != authImportPath || !isPackageLevelObjKind(obj) {
			return
		}
		qualifier := " (dot-imported)"
		if selfPackage {
			qualifier = " (same-package self-reference)"
		}
		diags = append(diags, Diagnostic{
			Rel:     rel,
			Line:    p.Fset.Position(id.Pos()).Line,
			Message: formatBannedSymbolDiag(authImportPath, obj.Name(), qualifier),
		})
	})
	return diags
}

// scanBannedRedeclarations is form (C): package-scope re-declaration inside
// authImportPath (`const RoleInternalAdmin = "..."` / `func BuiltinServiceRoles(...)`)
// that would otherwise sit as a dead-but-declared symbol (zero usage outside ⇒
// (A)/(B) silent), breaking the "re-introduction fails CI" contract. Local scope
// (function bodies, struct fields) is excluded via the package-scope check.
func scanBannedRedeclarations(p *Pass, file *ast.File, rel, authImportPath string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
		if !deletedAuthSymbols[id.Name] {
			return
		}
		obj := p.TypesInfo.Defs[id]
		if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != authImportPath {
			return
		}
		// Package scope only — local vars / params / struct fields that happen
		// to share a name are not re-introductions of the deleted symbol.
		if obj.Parent() == nil || obj.Parent() != obj.Pkg().Scope() || !isPackageLevelObjKind(obj) {
			return
		}
		diags = append(diags, Diagnostic{
			Rel:     rel,
			Line:    p.Fset.Position(id.Pos()).Line,
			Message: formatBannedSymbolDeclDiag(authImportPath, obj.Name()),
		})
	})
	return diags
}

// isPackageLevelObjKind reports whether obj is one of the four package-level
// object kinds the deleted-symbol scan recognizes (Func / Const / Var / TypeName).
func isPackageLevelObjKind(obj types.Object) bool {
	switch obj.(type) {
	case *types.Func, *types.Const, *types.Var, *types.TypeName:
		return true
	default:
		return false
	}
}

// formatBannedSymbolDiag composes the diagnostic message for a banned symbol
// reference. The qualifier suffix differentiates the three reference shapes
// — (A) qualified `pkg.X`, (B) dot-imported bare, (B) same-package self-ref
// — so log readers can tell at a glance which AST form triggered.
func formatBannedSymbolDiag(authImportPath, name, qualifier string) string {
	return fmt.Sprintf(
		"deprecated symbol %s.%s%s — replace with auth.RequireCallerCell (authz) "+
			"or auth.TestServiceContext (test principals); see PR #362 SVCTOKEN-CALLER-IDENTITY",
		lastPathSegment(authImportPath), name, qualifier,
	)
}

// formatBannedSymbolDeclDiag composes the diagnostic message for a
// package-scope re-declaration of a deleted symbol inside authImportPath.
// Distinct from formatBannedSymbolDiag because the actionable advice differs
// (delete the re-introduced declaration; do not just rewrite call sites).
func formatBannedSymbolDeclDiag(authImportPath, name string) string {
	return fmt.Sprintf(
		"deleted symbol %s.%s re-declared at package scope — delete the declaration; "+
			"these symbols were removed in Wave 2 of SVCTOKEN-CALLER-IDENTITY (PR #362) "+
			"and callers must migrate to auth.RequireCallerCell (authz) or "+
			"auth.TestServiceContext (test principals)",
		lastPathSegment(authImportPath), name,
	)
}

// productionScanPatterns is the seven production roots scanned by both the
// main invariant test and the BS-1 reverse self-check.
var productionScanPatterns = []string{
	"./runtime/...",
	"./corecells/...",
	"./cmd/...",
	"./kernel/...",
	"./adapters/...",
	"./examples/...",
	"./tests/...",
}

// CheckNoDeletedAuthSymbols01 runs NO-DELETED-AUTH-SYMBOLS-01 over the
// running module and returns its diagnostics. It is the importable rule body;
// GoCell's TestNO_DELETED_AUTH_SYMBOLS_01 calls it directly — single source,
// no parallel rule body.
//
// Not registered in StandardCellRules: allowlist is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension → would false-red an external cell's
// own auth code; kept importable & module-path-agnostic but vacuous-green
// externally.
func CheckNoDeletedAuthSymbols01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()

	return Run(t, WorkspaceTyped(
		TypedOpts{Tests: true, Tags: cfg.BuildTags},
		productionScanPatterns,
	),
		func(p *Pass) []Diagnostic {
			return scanDeletedAuthSymbolsAgainst(p, authRuntimeImportPath)
		})
}
