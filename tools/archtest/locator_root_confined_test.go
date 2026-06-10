//go:build archtest

// INVARIANT: LOCATOR-ROOT-CONFINED-01
//
// kernel/metadata AND tools/workspace production code must NOT call os.DirFS.
// Every disk-backed read of workspace metadata (.gocell/manifest.yaml, cell /
// slice / contract YAML) goes through an os.Root-confined fs.FS:
//   - kernel/metadata.NewLocator builds its fs.FS via os.OpenRoot(root).FS();
//   - kernel/metadata.ReadManifestModulePathsRoot opens an os.Root for the
//     manifest cross-check;
//   - tools/workspace calls only those root-confined entry points.
//
// os.Root confines Stat / WalkDir / ReadFile to root and rejects any path that
// escapes via a symlink ("path escapes from parent") at the syscall layer.
// os.DirFS, by contrast, FOLLOWS symlinks — a symlinked .gocell/manifest.yaml or
// module dir could read cells/contracts from outside the repo (#1592 + review
// F1). Banning os.DirFS across the whole workspace-root metadata boundary keeps a
// future caller from reintroducing the symlink-following primitive on a path the
// NewLocator / Modules regression tests cannot see.
//
// COVERED FORMS: both a direct call os.DirFS(root) AND a func-value reference
// `f := os.DirFS` (laundering) are flagged — Pass 2 resolves every os.DirFS
// selector and flags any that is not a direct call's Fun.
//
// RATING: Medium (downstream callsite ban). go/types resolves each call's callee
// to the canonical os.DirFS (import-alias / dot-import immune via TypesInfo.Uses).
// It backs the Hard runtime confinement of os.Root (a symlink escape is
// unexpressible at run time). Upstream is a Go ceiling — the language cannot
// express "every disk-backed metadata fs.FS must be root-confined" — so the
// os.Root construction sites + this downstream ban are the enforcement.
//
// BLIND SPOTS (accepted Medium gaps; no current code uses these forms):
//  1. A different symlink-following fs primitive (e.g. a third-party rooted-but-
//     following fs). Only os.DirFS, the stdlib symlink-following root, is banned.
//
// REVERSE SELF-CHECK / ANTI-VACUITY: production has zero os.DirFS calls after
// root-confinement, so the positive control is a dedicated RED fixture
// (internal/locatorrootfixture) holding a direct call + a func-value reference.
// The fixture sub-run asserts BOTH forms are detected (>= 2) — proving the
// resolver and both passes work, decoupled from production so future hardening
// cannot silently vacate this guard.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

const (
	locatorRootConfinedRule = "LOCATOR-ROOT-CONFINED-01"
	// metadataPkgPath ("…/kernel/metadata") is declared in
	// fixture_cellid_typed_builder_test.go and reused here.
	workspacePkgPath          = PlatformModulePath + "/tools/workspace"
	locatorRootFixturePattern = "./tools/archtest/internal/locatorrootfixture/..."
	locatorRootFixtureSuffix  = "/locatorrootfixture"
)

// TestLocatorRootConfined01 asserts kernel/metadata + tools/workspace production
// code never calls os.DirFS — disk-backed metadata reads must be os.Root-confined.
func TestLocatorRootConfined01(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping os.DirFS-based archtest in -short mode")
	}

	// Production: zero os.DirFS across the workspace-root metadata boundary.
	diags := Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		return scanDirFS(p, isMetadataBoundaryPkg(p.Pkg.Path()))
	})
	for _, diag := range diags {
		t.Errorf("%s", diag.Message)
	}

	// Anti-vacuity / reverse self-check: the RED fixture's direct call AND
	// func-value reference must both be detected (decoupled from production,
	// which is os.DirFS-free after root-confinement).
	fixtureDiags := Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{locatorRootFixturePattern}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || !strings.HasSuffix(p.Pkg.Path(), locatorRootFixtureSuffix) {
				return nil
			}
			return scanDirFS(p, true)
		})
	if len(fixtureDiags) < 2 {
		t.Fatalf("anti-vacuity: %s RED fixture must yield >= 2 os.DirFS detections "+
			"(direct call + func-value), got %d; resolver may have silently broken.",
			locatorRootConfinedRule, len(fixtureDiags))
	}
}

// isMetadataBoundaryPkg reports whether pkgPath performs disk-backed
// workspace-metadata reads and therefore must stay os.DirFS-free.
func isMetadataBoundaryPkg(pkgPath string) bool {
	return pkgPath == metadataPkgPath || pkgPath == workspacePkgPath
}

// scanDirFS flags os.DirFS usage (direct call + func-value reference) in p when
// flag is true. Shared by the production scan and the fixture positive control.
// Each Pass invocation owns its returned slice, so no synchronization is needed.
func scanDirFS(p *Pass, flag bool) []Diagnostic {
	if !flag || p.TypesInfo == nil {
		return nil
	}
	var d []Diagnostic
	for _, f := range p.Files {
		rel := p.Rel(f)
		// Pass 1: direct calls os.DirFS(...). Record each call's Fun selector
		// position so Pass 2 can tell calls apart from func-value references.
		callFunPos := map[token.Pos]bool{}
		EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
			if !isOsDirFSCall(p.TypesInfo, call) {
				return
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				callFunPos[sel.Sel.Pos()] = true
			}
			d = append(d, dirFSDiagnostic(rel, p.Fset.Position(call.Pos()).Line, "calls os.DirFS"))
		})
		// Pass 2 (closes the func-value-laundering blind spot): any os.DirFS
		// selector that is NOT a direct call's Fun is a func-value reference.
		EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
			if !isOsDirFSSelector(p.TypesInfo, sel) || callFunPos[sel.Sel.Pos()] {
				return
			}
			d = append(d, dirFSDiagnostic(rel, p.Fset.Position(sel.Pos()).Line,
				"references os.DirFS as a func value (func-value laundering)"))
		})
	}
	return d
}

// dirFSDiagnostic builds the standard LOCATOR-ROOT-CONFINED-01 message.
func dirFSDiagnostic(rel string, line int, what string) Diagnostic {
	return Diagnostic{
		Rel:  rel,
		Line: line,
		Message: fmt.Sprintf(
			locatorRootConfinedRule+": %s:%d %s — disk-backed workspace-metadata reads "+
				"must use an os.Root-confined fs.FS (os.OpenRoot(root).FS() via NewLocator / "+
				"ReadManifestModulePathsRoot), not os.DirFS which follows symlinks escaping "+
				"the workspace root (#1592).",
			rel, line, what,
		),
	}
}

// isOsDirFSCall reports whether call invokes the canonical os.DirFS function.
func isOsDirFSCall(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && isOsDirFSSelector(info, sel)
}

// isOsDirFSSelector reports whether sel resolves to the canonical os.DirFS
// function. Uses go/types so import aliases and dot-imports cannot disguise it.
func isOsDirFSSelector(info *types.Info, sel *ast.SelectorExpr) bool {
	if info == nil {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok {
		return false
	}
	return fn.Pkg() != nil && fn.Pkg().Path() == "os" && fn.Name() == "DirFS"
}
