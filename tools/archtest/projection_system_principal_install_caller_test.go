// INVARIANT: PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01
//
// PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01 — every production reference to
// projection.InstallSystemPrincipal MUST originate from the single sanctioned
// caller: the saga journal carrier's RestoreContext method,
// (*kernel/saga/sagaprojection.sagaProjectionEvent).RestoreContext.
//
// # Why this funnel exists
//
// projection.InstallSystemPrincipal is an OVERWRITE setter: it unconditionally
// replaces all four principal ctx keys (actor/subject/tenant/session) with the
// "system" sentinel — bypassing the auth trust boundary that outbox's no-overwrite
// RestoreContext honors. Unrestricted use could silently strip a legitimate
// authenticated identity from a request context; restricting it to the one site
// that has a principled reason (saga journal carrier, ADR #1609 §5) prevents
// misuse and keeps the impersonation threat matrix closed.
//
// # Two-axis AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream Hard: the scanner resolves every *ast.Ident whose object
//     (p.TypesInfo.Uses[id]) is the function projection.InstallSystemPrincipal,
//     then binds the reference to its enclosing FuncDecl identity
//     (types.Func.FullName via ResolveEnclosingFunc) and checks that identity
//     against a CALLSITE-level allowlist. Because matching is by go/types object
//     (not by AST surface form), it is invariant to every import shape —
//     qualified `projection.InstallSystemPrincipal`, aliased import, dot-import
//     bare identifier, AND same-package bare identifier inside kernel/projection
//     all resolve to the same *types.Func and are caught. The allowlist is keyed
//     by enclosing (*recv).Method / pkg.Func full name, so adding any OTHER
//     reference — even in the already-sanctioned source.go file — fails CI; the
//     funnel is symbol-and-callsite level, not file level.
//   - Upstream Medium (permanent Go-language ceiling): InstallSystemPrincipal is
//     an exported function in package kernel/projection; Go visibility cannot
//     express "only sagaprojection may reference this exported func". The
//     downstream archtest is the enforcement backstop. Hard-upgrade path = seal
//     InstallSystemPrincipal behind an unexported interface or move it to an
//     internal/ sub-package so sagaprojection is the only import-reachable caller.
//     Tracked at gh #1702 (won't-do-now — sole caller today is the saga carrier,
//     no second caller risk).
//
// # Blind spots and anti-vacuity
//
//   - References resolved through go/types Uses cover qualified, aliased,
//     dot-imported, same-package-bare, and function-value-capture forms
//     (`f := projection.Install…; f(ctx)` — the SelectorExpr's Sel ident is a
//     Uses entry; `f := Install…` in-package — the bare ident is a Uses entry).
//     None of these is a blind spot.
//   - Build-tag-gated production files under non-default tags are not scanned by
//     the default-context load. No such file references InstallSystemPrincipal
//     today.
//   - A reference whose enclosing FuncDecl cannot be resolved (package-level var
//     / const initializer) is treated as a hard violation, not silently skipped.
//
// The anti-vacuity reverse self-check (every callsite allowlist entry must have
// ≥1 observed live reference) ensures the allowlist cannot silently become a
// dead bypass slot if the caller is ever removed or renamed.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// systemPrincipalInstallPkg is the canonical import path of kernel/projection,
// where InstallSystemPrincipal is declared.
const systemPrincipalInstallPkg = PlatformModulePath + "/kernel/projection"

// systemPrincipalInstallFunc is the name of the function being locked.
const systemPrincipalInstallFunc = "InstallSystemPrincipal"

// sagaProjectionPkg is the canonical import path of kernel/saga/sagaprojection,
// the package owning the sole sanctioned caller.
const sagaProjectionPkg = PlatformModulePath + "/kernel/saga/sagaprojection"

// systemPrincipalInstallCallerAllowlist is the set of caller identities
// (types.Func.FullName) permitted to reference projection.InstallSystemPrincipal.
//
// Only the saga journal carrier's RestoreContext method is sanctioned: it is the
// sole site that intentionally replaces the ambient principal with the "system"
// sentinel, justified by ADR #1609 §5 (saga replay runs under system identity,
// not the triggering admin's identity). The key is the enclosing-function full
// name (not a file path), so any other reference — including a second reference
// added inside source.go — fails CI.
var systemPrincipalInstallCallerAllowlist = map[string]struct{}{
	"(*" + sagaProjectionPkg + ".sagaProjectionEvent).RestoreContext": {},
}

// TestProjectionSystemPrincipalInstallCaller01 asserts that every production
// reference to projection.InstallSystemPrincipal originates from a caller in the
// sanctioned callsite allowlist, and that no allowlist entry is stale
// (anti-vacuity reverse check).
func TestProjectionSystemPrincipalInstallCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanInstallSystemPrincipalCallers(p, observed)
	})
	// Anti-vacuity: every allowlist entry must have ≥1 live observed reference.
	diags = append(diags, staleInstallAllowlistDiags(observed)...)
	Report(t, "PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01", diags)
}

// scanInstallSystemPrincipalCallers records the enclosing-function identity of
// every reference to projection.InstallSystemPrincipal into observed and returns
// a diagnostic for each reference whose enclosing caller is outside the allowlist.
//
// Resolution is by go/types object: it walks every *ast.Ident named
// InstallSystemPrincipal and matches when p.TypesInfo.Uses[id] is the *types.Func
// declared in kernel/projection. This is form-invariant — qualified selectors
// (the Sel ident is a Uses entry), dot-import bare idents, and same-package bare
// idents are all caught. Definition idents (the `func InstallSystemPrincipal`
// declaration) live in Defs, not Uses, so the declaration site is not flagged.
func scanInstallSystemPrincipalCallers(p *Pass, observed map[string]struct{}) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
			if id.Name != systemPrincipalInstallFunc {
				return
			}
			fn, ok := p.TypesInfo.Uses[id].(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg().Path() != systemPrincipalInstallPkg {
				return
			}
			line := p.Fset.Position(id.Pos()).Line
			caller, ok := ResolveEnclosingFunc(p.TypesInfo, file, id)
			if !ok {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: "PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01: " +
						"projection.InstallSystemPrincipal is referenced outside any " +
						"FuncDecl (package-level var/const initializer or similar) — this " +
						"cannot be allowlisted at the callsite level. Move the reference " +
						"into the sanctioned caller " +
						"((*kernel/saga/sagaprojection.sagaProjectionEvent).RestoreContext).",
				})
				return
			}
			callerID := caller.FullName()
			observed[callerID] = struct{}{}
			if _, allowed := systemPrincipalInstallCallerAllowlist[callerID]; allowed {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01: "+
						"projection.InstallSystemPrincipal is referenced from caller %q, which "+
						"is not a sanctioned caller. This function unconditionally overwrites "+
						"all four principal ctx keys with the \"system\" sentinel — bypassing "+
						"the auth trust boundary. Only the saga journal carrier "+
						"((*kernel/saga/sagaprojection.sagaProjectionEvent).RestoreContext) is "+
						"permitted to reference it. See ADR #1609 §5 and gh #1702 for the "+
						"Hard-upgrade tracking issue.",
					callerID,
				),
			})
		})
	}
	return diags
}

// staleInstallAllowlistDiags is the anti-vacuity reverse self-check: every
// callsite allowlist entry must correspond to at least one live observed
// reference (keyed by enclosing-function full name).
func staleInstallAllowlistDiags(observed map[string]struct{}) []Diagnostic {
	var diags []Diagnostic
	for caller := range systemPrincipalInstallCallerAllowlist {
		if _, seen := observed[caller]; seen {
			continue
		}
		diags = append(diags, Diagnostic{
			Message: fmt.Sprintf(
				"PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01: allowlist entry %q is STALE "+
					"— no live reference to projection.InstallSystemPrincipal observed from that "+
					"caller. Either the scanner regressed or the call was removed/renamed; drop "+
					"the dead allowlist entry so it cannot become a silent bypass slot.",
				caller,
			),
		})
	}
	return diags
}

// TestProjectionSystemPrincipalInstallCaller01_RedFixture is the negative control
// for the funnel: it verifies the scanner FIRES on a deliberate violation — a
// bare-identifier (dot-imported) call to InstallSystemPrincipal from an
// unsanctioned caller in internal/systemprincipalinstallfixture. This proves the
// funnel rejects (not merely that anti-vacuity observes the sanctioned caller),
// and that bare-ident / dot-import forms are resolved by go/types object (not AST
// surface form). The pre-fix SelectorExpr-only scanner would have produced 0 here.
func TestProjectionSystemPrincipalInstallCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	throwaway := map[string]struct{}{}
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/systemprincipalinstallfixture"}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			found += len(scanInstallSystemPrincipalCallers(p, throwaway))
			return nil
		})
	assert.Equal(t, 1, found,
		"PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01 RED fixture self-check FAILED: "+
			"expected exactly 1 violation from systemprincipalinstallfixture "+
			"(bypassInstallFromUnsanctionedCaller calls InstallSystemPrincipal via a "+
			"dot-imported bare identifier from a non-sanctioned caller). Got %d — found<1 "+
			"means the scanner missed the bare-ident/dot-import form (SelectorExpr-only "+
			"regression); found>1 means over-detection. This fixture is the negative "+
			"control for the symbol+callsite-level funnel.", found)
}

// TestProjectionSystemPrincipalInstallCaller01_NoDotImport is a complementary
// defense-in-depth form-ban for PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01.
//
// # Relationship to the main scanner
//
// The main scanner (TestProjectionSystemPrincipalInstallCaller01) resolves
// references by go/types object, so a dot-imported bare-identifier CALL to
// InstallSystemPrincipal is already caught there — dot-import is no longer a
// blind spot of the funnel. This sub-test is therefore NOT the sole dot-import
// guard; it adds a strictly broader, structural form-ban: it forbids the
// `import . "…/kernel/projection"` form in any production file regardless of
// whether InstallSystemPrincipal is referenced, so a setup-for-bypass import
// cannot even land before a call is added. Dot-importing kernel/projection is
// unusual and conspicuous; banning the form keeps the surface minimal.
//
// # Scope
//
// Scans all production files (excluding _test.go). kernel/projection itself is
// excluded: it is the package owner and legitimately declares its own symbols.
//
// # Blind spots of this sub-test
//
// Transitive dot-import via an intermediate package is not detected (would require
// full import graph traversal). No such file exists today; this test covers direct
// dot-import of kernel/projection, which is the realistic form to ban.
func TestProjectionSystemPrincipalInstallCaller01_NoDotImport(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const projectionPkgSuffix = "kernel/projection"

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		// Exclude the kernel/projection package itself.
		if p.Pkg != nil && strings.HasSuffix(p.Pkg.Path(), projectionPkgSuffix) {
			return nil
		}
		var fileDiags []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.ImportSpec](file, func(spec *ast.ImportSpec) {
				if spec.Name == nil || spec.Name.Name != "." {
					return
				}
				path := strings.Trim(spec.Path.Value, `"`)
				if !strings.HasSuffix(path, projectionPkgSuffix) {
					return
				}
				fileDiags = append(fileDiags, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(spec.Pos()).Line,
					Message: fmt.Sprintf(
						"PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01 (dot-import form-ban): "+
							"%s uses dot-import of kernel/projection "+
							"(import . %q). Even though the main scanner resolves dot-imported "+
							"references by symbol, this conspicuous import form is banned outright "+
							"as defense-in-depth. Use a qualified import instead: "+
							"import \"…/kernel/projection\" and call projection.InstallSystemPrincipal.",
						rel, spec.Path.Value,
					),
				})
			})
		}
		return fileDiags
	})
	Report(t, "PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01/dot-import", diags)
}
