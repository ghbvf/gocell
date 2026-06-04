// INVARIANT: LOCATOR-ROOT-CONFINED-01
//
// kernel/metadata production code must NOT call os.DirFS. The disk-backed
// metadata Locator (NewLocator) builds its fs.FS via os.OpenRoot(root).FS(),
// which confines every Stat / WalkDir / ReadFile to the directory tree at root
// and rejects any path that escapes via a symlink ("path escapes from parent")
// at the syscall layer. A manifest module path (or any subpath) that is a
// symlink escaping the workspace would otherwise make discovery read
// cells/contracts from outside the repo (#1592). Banning os.DirFS in
// kernel/metadata prevents reintroducing the symlink-following primitive on a
// NEW construction path that the NewLocator-level regression test
// (kernel/metadata.TestLocator_RootConfinement_SymlinkEscape) cannot see.
//
// SCOPE: only kernel/metadata is flagged. tools/workspace also calls os.DirFS
// (ReadManifestModulePaths cross-check + memberHasMetadata conventional scan),
// but those are safe by composition and are intentionally NOT flagged — they
// also serve as the anti-vacuity positive control:
//   - memberHasMetadata runs the CONVENTIONAL Locator, whose WalkDir never
//     recurses into symlink entries (DirEntry.IsDir() is false for a symlink),
//     so no symlink is ever followed regardless of the underlying fs.FS.
//   - the manifest module dirs fed to ReadManifestModulePaths are gated by the
//     forward cross-check manifest.modules ⊆ go.work.use, and the go.work `use`
//     dirs are symlink-escape-guarded by gomodutil.ReadWorkUseDirs (#1555),
//     so a symlinked manifest module path cannot pass the cross-check.
//
// RATING: Medium (downstream callsite ban). go/types resolves each call's callee
// to the canonical os.DirFS (import-alias / dot-import immune via
// TypesInfo.Uses). It backs the Hard runtime confinement provided by os.Root:
// the syscall layer makes a symlink escape unexpressible at run time; this
// archtest keeps kernel/metadata from swapping back to the symlink-following
// os.DirFS. Upstream is a Go ceiling — the language cannot express "every
// disk-backed metadata fs.FS must be root-confined" — so the single NewLocator
// construction site + this downstream ban are the enforcement.
//
// COVERED FORMS in kernel/metadata: both a direct call os.DirFS(root) AND a
// func-value reference `f := os.DirFS` (laundering) are flagged — the reverse
// self-check below resolves every os.DirFS selector and flags any that is not a
// direct call's Fun, so func-value laundering cannot disguise the primitive.
//
// BLIND SPOTS (accepted Medium gaps; no current code uses these forms):
//  1. A different symlink-following fs primitive (e.g. a third-party rooted-but-
//     following fs). Only os.DirFS, the stdlib symlink-following root, is banned.
//
// REVERSE SELF-CHECK: (a) the scan counts every resolved os.DirFS call tree-wide
// and asserts >= 1 (the tools/workspace callsites) — if callee resolution
// silently broke, that count would be 0 and the test fails; (b) the func-value
// branch scans every os.DirFS selector that is not a call Fun, closing the
// laundering blind spot rather than only documenting it.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sync"
	"testing"
)

// metadataPkgPath ("github.com/ghbvf/gocell/kernel/metadata") is declared in
// fixture_cellid_typed_builder_test.go and reused here.
const locatorRootConfinedRule = "LOCATOR-ROOT-CONFINED-01"

// TestLocatorRootConfined01 asserts kernel/metadata production code never calls
// os.DirFS — the disk-backed Locator must build its fs.FS via os.OpenRoot.FS().
func TestLocatorRootConfined01(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping os.DirFS-based archtest in -short mode")
	}

	var (
		mu       sync.Mutex
		resolved int
	)
	diags := Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		var d []Diagnostic
		flagged := p.Pkg.Path() == metadataPkgPath
		for _, f := range p.Files {
			rel := p.Rel(f)
			// Pass 1: direct calls os.DirFS(...). Record each call's Fun selector
			// position so Pass 2 can tell calls apart from func-value references.
			callFunPos := map[token.Pos]bool{}
			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				if !isOsDirFSCall(p.TypesInfo, call) {
					return
				}
				mu.Lock()
				resolved++
				mu.Unlock()
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					callFunPos[sel.Sel.Pos()] = true
				}
				if flagged {
					d = append(d, dirFSDiagnostic(rel, p.Fset.Position(call.Pos()).Line, "calls os.DirFS"))
				}
			})
			if !flagged {
				continue
			}
			// Pass 2 (reverse self-check, closes the func-value-laundering blind
			// spot): any os.DirFS selector that is NOT a direct call's Fun is a
			// func-value reference (`f := os.DirFS`) that Pass 1 cannot see.
			EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
				if !isOsDirFSSelector(p.TypesInfo, sel) || callFunPos[sel.Sel.Pos()] {
					return
				}
				d = append(d, dirFSDiagnostic(rel, p.Fset.Position(sel.Pos()).Line,
					"references os.DirFS as a func value (func-value laundering)"))
			})
		}
		return d
	})

	for _, diag := range diags {
		t.Errorf("%s", diag.Message)
	}
	if resolved < 1 {
		t.Fatalf("anti-vacuity: %s resolved 0 os.DirFS calls tree-wide; "+
			"callee resolution may have silently broken.", locatorRootConfinedRule)
	}
}

// dirFSDiagnostic builds the standard LOCATOR-ROOT-CONFINED-01 message.
func dirFSDiagnostic(rel string, line int, what string) Diagnostic {
	return Diagnostic{
		Rel:  rel,
		Line: line,
		Message: fmt.Sprintf(
			locatorRootConfinedRule+": %s:%d %s in kernel/metadata. The disk-backed "+
				"Locator must build its fs.FS via os.OpenRoot(root).FS() (NewLocator) so "+
				"discovery is confined to the workspace root and cannot follow a symlink "+
				"escaping it (#1592).",
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
