// INVARIANT: PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01
//
// PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01 — every production callsite of
// projection.InstallSystemPrincipal MUST reside in the single sanctioned caller:
// kernel/saga/sagaprojection (the saga journal carrier's RestoreContext).
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
//   - Downstream Hard: archtest caller-allowlist via go/types (ResolvePackageRef)
//     resolves `projection.InstallSystemPrincipal` regardless of alias or
//     dot-import form. Any call outside the allowlist fails CI immediately.
//   - Upstream Medium (permanent Go-language ceiling): InstallSystemPrincipal is
//     an exported function in package kernel/projection; Go visibility cannot
//     express "only sagaprojection may call this exported func". The downstream
//     archtest is the enforcement backstop. Hard-upgrade path = seal
//     InstallSystemPrincipal behind an unexported interface or move it to an
//     internal/ sub-package so sagaprojection is the only import-reachable caller.
//     Tracked at gh #1702 (won't-do-now — sole caller today is sagaprojection,
//     no second caller risk).
//
// # Blind spots and anti-vacuity
//
//   - Dot-import bare-identifier form (import . "…/kernel/projection";
//     InstallSystemPrincipal(ctx)) would appear as a bare *ast.Ident, not a
//     SelectorExpr, and would be missed. Dot-importing kernel/projection is
//     unusual and conspicuous; the anti-vacuity guard makes a missing live call
//     in the allowlisted file visible immediately.
//   - A call hidden behind a function-value variable (f := projection.Install…;
//     f(ctx)) is matched: the scanner is reference-based (every SelectorExpr the
//     type-checker resolves to the target symbol), not call-only.
//   - Build-tag-gated production files under non-default tags are missed. No
//     such file exists today.
//
// The anti-vacuity reverse self-check (every allowlist entry must have ≥1
// observed reference) ensures the allowlist cannot silently become a dead bypass
// slot if the caller is ever removed or renamed.
package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"
)

// systemPrincipalInstallPkg is the canonical import path of kernel/projection,
// where InstallSystemPrincipal is declared.
const systemPrincipalInstallPkg = PlatformModulePath + "/kernel/projection"

// systemPrincipalInstallFunc is the name of the function being locked.
const systemPrincipalInstallFunc = "InstallSystemPrincipal"

// systemPrincipalInstallAllowlist is the set of module-relative production
// files permitted to call projection.InstallSystemPrincipal.
//
// Only the saga journal carrier's RestoreContext is sanctioned: it is the sole
// site that intentionally replaces the ambient principal with the "system"
// sentinel, justified by ADR #1609 §5 (saga replay runs under system identity,
// not the triggering admin's identity).
var systemPrincipalInstallAllowlist = map[string]struct{}{
	"kernel/saga/sagaprojection/source.go": {},
}

// TestProjectionSystemPrincipalInstallCaller01 asserts that every production
// callsite of projection.InstallSystemPrincipal sits in the sanctioned allowlist,
// and that no allowlist entry is stale (anti-vacuity reverse check).
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

// scanInstallSystemPrincipalCallers records every reference to
// projection.InstallSystemPrincipal into observed and returns a diagnostic for
// each reference outside the allowlist.
func scanInstallSystemPrincipalCallers(p *Pass, observed map[string]struct{}) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, sel)
			if !ok || pkgPath != systemPrincipalInstallPkg || name != systemPrincipalInstallFunc {
				return
			}
			observed[rel] = struct{}{}
			if _, allowed := systemPrincipalInstallAllowlist[rel]; allowed {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(sel.Pos()).Line,
				Message: fmt.Sprintf(
					"PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01: "+
						"projection.InstallSystemPrincipal is called from %s, which is not a "+
						"sanctioned caller. This function unconditionally overwrites all four "+
						"principal ctx keys with the \"system\" sentinel — bypassing the auth "+
						"trust boundary. Only the saga journal carrier "+
						"(kernel/saga/sagaprojection/source.go) is permitted to call it. "+
						"See ADR #1609 §5 and gh #1702 for the Hard-upgrade tracking issue.",
					rel,
				),
			})
		})
	}
	return diags
}

// staleInstallAllowlistDiags is the anti-vacuity reverse self-check: every
// allowlist entry must correspond to at least one live observed reference.
func staleInstallAllowlistDiags(observed map[string]struct{}) []Diagnostic {
	var diags []Diagnostic
	for f := range systemPrincipalInstallAllowlist {
		if _, seen := observed[f]; seen {
			continue
		}
		diags = append(diags, Diagnostic{
			Message: fmt.Sprintf(
				"PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01: allowlist entry %q is STALE "+
					"— no live reference to projection.InstallSystemPrincipal observed in that file. "+
					"Either the scanner regressed or the call was removed; drop the dead "+
					"allowlist entry so it cannot become a silent bypass slot.",
				f,
			),
		})
	}
	return diags
}

// TestProjectionSystemPrincipalInstallCaller01_NoDotImport is the dot-import
// blind-spot anti-vacuity guard for PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01.
//
// # Why this sub-test is needed
//
// TestProjectionSystemPrincipalInstallCaller01 uses EachInSubtree[ast.SelectorExpr]
// which only matches qualified `pkg.Func` call forms. A file using dot-import
// (import . "…/kernel/projection") can call InstallSystemPrincipal as a bare
// identifier — an *ast.Ident, not an *ast.SelectorExpr — and would be invisible
// to the main scanner.
//
// This sub-test closes that vector by scanning all production ImportSpec nodes
// for the dot-import form (Name == ".") pointing at the kernel/projection package.
// Any such file would be a hard-to-detect bypass; the test fails CI immediately
// rather than waiting for the SelectorExpr scanner to miss it.
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
// dot-import of kernel/projection, which is the realistic bypass vector.
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
						"PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01 (dot-import blind spot): "+
							"%s uses dot-import of kernel/projection "+
							"(import . %q). This form makes InstallSystemPrincipal callable as a "+
							"bare identifier, bypassing the SelectorExpr scanner. "+
							"Use a qualified import instead: "+
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
