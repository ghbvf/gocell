// INVARIANT: NO-DELETED-AUTH-SYMBOLS-01
//
// # NO-DELETED-AUTH-SYMBOLS-01
//
// Invariant: no production or test .go file (outside the canonical
// definition site under runtime/auth/) may reference the deleted symbols
//   - auth.RoleInternalAdmin
//   - auth.ServiceNameInternal
//   - auth.BuiltinServiceRoles
//
// These symbols were removed in Wave 2 of the SVCTOKEN-CALLER-IDENTITY
// migration (PR #362 "A5 service token caller_cell + contract.clients
// runtime enforce"). The rule enforces a "0 references" check so
// re-introduction at any call site fails CI.
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
// Two AST forms together cover every cross-package reference to a banned
// identifier — qualified or dot-imported, const / var / func / typename:
//   - (A) qualified SelectorExpr `pkg.X` — alias-transparent via
//     info.Uses[sel.X].(*types.PkgName).Imported().Path() (covers consts +
//     funcs at any expression position, including value-capture
//     `var fn = auth.BuiltinServiceRoles`). Resolution delegates to
//     ResolvePackageRef typed façade.
//   - (B) bare *ast.Ident — covers dot-imported references (`import .
//     "...runtime/auth"; _ = RoleInternalAdmin / _ = ServiceNameInternal /
//     BuiltinServiceRoles(...)`). Uses info.Uses[id] directly with type
//     switch over {*types.Const, *types.Var, *types.Func, *types.TypeName},
//     bypassing ResolvePackageRef's typed-callable filter (which intentionally
//     rejects Const/Var per its godoc). Same-package self-references inside
//     authImportPath are excluded by the pass-level filter at function entry.
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
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ruleNoDeletedAuthSymbols01 = "NO-DELETED-AUTH-SYMBOLS-01"

// fixtureAuthImportPath is the canonical path of the fixture-local fake
// `auth` package used by TestNO_DELETED_AUTH_SYMBOLS_01_FixtureCatchesAllForms.
// It is intentionally distinct from authRuntimeImportPath (defined in
// svctoken_caller_cell_test.go) so the same scanner function exercises both
// targets without redeclaring constants in production scope.
const fixtureAuthImportPath = "github.com/ghbvf/gocell/tools/archtest/internal/nodeletedauthsymbolsfixture/auth"

// deletedAuthSymbols is the set of selector / identifier names that must not
// appear in any reference outside the canonical definition site.
var deletedAuthSymbols = map[string]bool{
	"RoleInternalAdmin":   true,
	"ServiceNameInternal": true,
	"BuiltinServiceRoles": true,
}

// scanDeletedAuthSymbolsAgainst walks a typed Pass and records every
// reference to a banned identifier whose owning package resolves to
// authImportPath. Parametrized over authImportPath so the production
// invariant test (authRuntimeImportPath) and the fixture self-check
// (fixtureAuthImportPath) share one implementation.
//
// Passes whose own package IS authImportPath are skipped — the canonical
// definition site is allowed to use its own symbols (the rule guards
// external references, not the def site's own scope).
func scanDeletedAuthSymbolsAgainst(p *Pass, authImportPath string) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	if p.Pkg != nil && p.Pkg.Path() == authImportPath {
		// Canonical definition site — same-package self-references are
		// in-scope by design (e.g. auth.go's `return []string{
		// RoleInternalAdmin}` inside BuiltinServiceRoles).
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)

		// Pre-collect the positions of SelectorExpr.Sel so the bare-Ident
		// scan below does not double-count an Ident that is already the Sel
		// half of a qualified selector caught by (A). Without this guard,
		// EachInSubtree[ast.Ident] would visit `auth.RoleInternalAdmin`'s
		// `RoleInternalAdmin` Sel-Ident and resolve it (via info.Uses) to the
		// same *types.Const / *types.Func that (A) already recorded — every
		// qualified reference would be reported twice.
		selSelPositions := make(map[token.Pos]bool)
		EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
			if sel.Sel != nil {
				selSelPositions[sel.Sel.Pos()] = true
			}
		})

		// (A) Qualified SelectorExpr — alias-transparent via ResolvePackageRef.
		EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, sel)
			if !ok || pkgPath != authImportPath {
				return
			}
			if !deletedAuthSymbols[name] {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:     rel,
				Line:    p.Fset.Position(sel.Pos()).Line,
				Message: formatBannedSymbolDiag(authImportPath, name, false),
			})
		})

		// (B) Bare *ast.Ident — dot-imported Const / Var / Func / TypeName.
		// Uses info.Uses[id] directly with type switch over the four
		// package-level object kinds so dot-imported const/var bare refs are
		// detected (ResolvePackageRef intentionally rejects Const/Var per
		// its typed-callable filter; that limitation is bypassed at caller
		// site here, keeping the façade unchanged — façade extension is
		// tracked separately by #1037).
		EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
			if selSelPositions[id.Pos()] {
				return
			}
			if !deletedAuthSymbols[id.Name] {
				return
			}
			obj := p.TypesInfo.Uses[id]
			if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != authImportPath {
				return
			}
			switch obj.(type) {
			case *types.Func, *types.Const, *types.Var, *types.TypeName:
				// covered
			default:
				return
			}
			diags = append(diags, Diagnostic{
				Rel:     rel,
				Line:    p.Fset.Position(id.Pos()).Line,
				Message: formatBannedSymbolDiag(authImportPath, obj.Name(), true),
			})
		})
	}
	return diags
}

// formatBannedSymbolDiag composes the diagnostic message for a banned symbol
// reference. The dotImported suffix differentiates (A) qualified vs (B)
// dot-imported hits so log readers can tell at a glance which AST form
// triggered.
func formatBannedSymbolDiag(authImportPath, name string, dotImported bool) string {
	qualifier := ""
	if dotImported {
		qualifier = " (dot-imported)"
	}
	return fmt.Sprintf(
		"deprecated symbol %s.%s%s — replace with auth.RequireCallerCell (authz) "+
			"or auth.TestServiceContext (test principals); see PR #362 SVCTOKEN-CALLER-IDENTITY",
		shortPkg(authImportPath), name, qualifier)
}

// productionScanPatterns is the seven production roots scanned by both the
// main invariant test and the BS-1 reverse self-check.
var productionScanPatterns = []string{
	"./runtime/...",
	"./cells/...",
	"./cmd/...",
	"./kernel/...",
	"./adapters/...",
	"./examples/...",
	"./tests/...",
}

// TestNO_DELETED_AUTH_SYMBOLS_01 enforces that no production or test code
// references the three deleted runtime/auth symbols. The scan runs typed
// over the seven production roots, paired with FlatNonDefaultTags() to cover
// build-tagged variants (e.g. //go:build integration test helpers).
func TestNO_DELETED_AUTH_SYMBOLS_01(t *testing.T) {
	t.Parallel()

	diags := RunTyped(t,
		TypedOpts{Tests: true, Tags: FlatNonDefaultTags()},
		productionScanPatterns,
		func(p *Pass) []Diagnostic {
			return scanDeletedAuthSymbolsAgainst(p, authRuntimeImportPath)
		})

	Report(t, ruleNoDeletedAuthSymbols01, diags)
}

// TestNO_DELETED_AUTH_SYMBOLS_01_FixtureCatchesAllForms exercises every
// import form the scanner must catch (default alias, custom alias,
// dot-imported func) and the false-positive form it must reject (same-name
// decoy from a different package). The fixture-local auth import path
// replaces the runtime/auth path so the same scanner code path runs
// unchanged.
//
// Expected hit breakdown (drift would break exact-count assertion):
//
//   - caller_default_alias.go      → 3 hits (2 consts + 1 func, default alias)
//   - caller_custom_alias.go       → 3 hits (2 consts + 1 func, custom alias)
//   - caller_dot_import.go         → 3 hits (2 consts + 1 func, dot-imported;
//     covered by (B) info.Uses type switch — Const/Var/Func/TypeName)
//   - caller_negative_other_pkg.go → 0 hits (same names, different package)
//   - caller_reflect_bs1.go        → 0 hits (BS-1 fixture; main scan ignores
//     string literals; verified by sibling BS-1 fixture test)
//   - doc.go                       → 0 hits (package documentation only)
//
// Per ai-robust.md §"Hard 范本": the fixture is a real Go package loaded via
// packages.Load with the archtest_fixture build tag. Bypassing this test
// requires modifying real source code.
func TestNO_DELETED_AUTH_SYMBOLS_01_FixtureCatchesAllForms(t *testing.T) {
	t.Parallel()

	diags := RunTypedFixture(t,
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/nodeletedauthsymbolsfixture/..."},
		func(p *Pass) []Diagnostic {
			return scanDeletedAuthSymbolsAgainst(p, fixtureAuthImportPath)
		})

	hitsByFile := map[string]int{}
	for _, d := range diags {
		hitsByFile[d.Rel]++
		t.Logf("fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}

	require.Len(t, diags, 9,
		"fixture must yield exactly 9 hits (3 default + 3 custom-alias + 3 dot-import + 0 negative + 0 reflect-bs1 + 0 doc); "+
			"any change in the fixture must update the expected count")

	// Per-file breakdown so a re-shuffled fixture cannot silently keep the
	// total constant while losing a form. doc.go and caller_reflect_bs1.go
	// assertions (0 hits each) catch silent drift if someone adds a banned
	// reference to those files.
	type expect struct {
		suffix string
		want   int
	}
	expectations := []expect{
		{"caller_default_alias.go", 3},
		{"caller_custom_alias.go", 3},
		{"caller_dot_import.go", 3},
		{"caller_negative_other_pkg.go", 0},
		{"caller_reflect_bs1.go", 0},
		{"doc.go", 0},
	}
	for _, e := range expectations {
		got := 0
		for rel, n := range hitsByFile {
			if strings.HasSuffix(rel, e.suffix) {
				got += n
			}
		}
		assert.Equal(t, e.want, got,
			"fixture %s must yield exactly %d hits; got %d (see logged diagnostics for actual)",
			e.suffix, e.want, got)
	}
}

// TestNO_DELETED_AUTH_SYMBOLS_01_BS1_NoReflectAccess implements the BS-1
// reverse self-check: no production code calls a reflect.* function with a
// string argument that contains one of the banned symbol names. This guards
// the obvious reflective-bypass pattern without requiring full reflect type
// tracing. Companion test
// TestNO_DELETED_AUTH_SYMBOLS_01_BS1_FixtureCatchesReflect verifies the
// scanner logic actually fires on a synthetic call site.
func TestNO_DELETED_AUTH_SYMBOLS_01_BS1_NoReflectAccess(t *testing.T) {
	t.Parallel()

	diags := RunTyped(t,
		TypedOpts{Tests: true, Tags: FlatNonDefaultTags()},
		productionScanPatterns,
		scanDeletedAuthSymbolsReflectBypass)

	assert.Empty(t, diags,
		"NO-DELETED-AUTH-SYMBOLS-01 BS-1: reflect call site references a banned "+
			"symbol name as a string literal; production code must not bypass the typed "+
			"funnel via reflection — remove the reflective lookup and call the replacement "+
			"API (auth.RequireCallerCell / auth.TestServiceContext) directly; "+
			"see PR #362 SVCTOKEN-CALLER-IDENTITY")
}

// TestNO_DELETED_AUTH_SYMBOLS_01_BS1_FixtureCatchesReflect pins the BS-1
// scanner's positive-detection contract: a synthetic reflect.<X>(...) call
// site whose string-literal argument names a banned symbol must produce
// exactly one diagnostic. Without this gate, a regression that disables the
// BS-1 scanner's string-match branch would leave the "no reflect access"
// assertion silently green forever (production scan stays at 0 hits regardless).
func TestNO_DELETED_AUTH_SYMBOLS_01_BS1_FixtureCatchesReflect(t *testing.T) {
	t.Parallel()

	diags := RunTypedFixture(t,
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/nodeletedauthsymbolsfixture/..."},
		scanDeletedAuthSymbolsReflectBypass)

	require.Len(t, diags, 2,
		"BS-1 fixture must yield exactly 2 hits (FieldByName + MethodByName chained off reflect.ValueOf "+
			"in caller_reflect_bs1.go); got %d", len(diags))
	for _, d := range diags {
		assert.Contains(t, d.Rel, "caller_reflect_bs1.go",
			"BS-1 fixture hit must originate in caller_reflect_bs1.go")
	}
	var sawField, sawMethod bool
	for _, d := range diags {
		if strings.Contains(d.Message, reflectFieldByName) {
			sawField = true
		}
		if strings.Contains(d.Message, reflectMethodByName) {
			sawMethod = true
		}
	}
	assert.True(t, sawField, "BS-1 fixture must produce a FieldByName diagnostic")
	assert.True(t, sawMethod, "BS-1 fixture must produce a MethodByName diagnostic")
}

// scanDeletedAuthSymbolsReflectBypass delegates to the shared
// scanReflectStringArgCalls (REFLECT-STRING-ARG-SCANNER-01) which
// type-checks the receiver to reflect.Value and folds constant string
// arguments. Covers both call forms (method-value `v.MethodByName(name)` +
// method-expression `reflect.Value.MethodByName(v, name)`) and both methods
// (FieldByName for value reads, MethodByName for method lookup), so chained
// shapes like `reflect.ValueOf(x).MethodByName("BuiltinServiceRoles")` are
// caught — closing what would otherwise be the BS-1a chained-reflect
// residual.
func scanDeletedAuthSymbolsReflectBypass(p *Pass) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	banned := func(n string) bool { return deletedAuthSymbols[n] }
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		for _, method := range []string{reflectFieldByName, reflectMethodByName} {
			for _, hit := range scanReflectStringArgCalls(p, file, method, banned) {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: hit.Line,
					Message: fmt.Sprintf(
						"NO-DELETED-AUTH-SYMBOLS-01 BS-1: reflect.Value.%s called with %q — banned symbol; "+
							"replace with auth.RequireCallerCell (authz) or auth.TestServiceContext (test principals); "+
							"see PR #362 SVCTOKEN-CALLER-IDENTITY",
						method, hit.Name),
				})
			}
		}
	}
	return diags
}
