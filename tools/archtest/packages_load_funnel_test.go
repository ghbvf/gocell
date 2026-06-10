//go:build archtest

// INVARIANT: PACKAGES-LOAD-FUNNEL-01
//
// golang.org/x/tools/go/packages.Load may be called directly ONLY from package
// github.com/ghbvf/gocell/tools/packagesload. Every other repo loader must go
// through packagesload.Load(mode, …), which forces an explicit workspace [Mode]
// (ModeModule = GOWORK=off / ModeWorkspace) so no loader silently inherits the
// ambient GOWORK from the committed repo-root go.work.
//
// WHY: PR #1554 commits a go.work (`use .`), putting `go` into workspace mode
// repo-wide. Loaders that analyze the root module or an isolated standalone
// module (archtest/depgraph testdata fixtures, release-pinned per-module builds)
// need ModeModule; a future cross-module archtest loader (Plan D) needs
// ModeWorkspace. Routing every load through one typed-mode funnel keeps both
// intents expressible — superseding the earlier blanket "every packages.Config
// must set GOWORK=off" ban (PACKAGES-CONFIG-GOWORK-OFF-01), which pre-judged
// future workspace-mode loaders.
//
// RATING: Medium (downstream caller-allowlist). go/types resolves each call's
// callee to the canonical golang.org/x/tools/go/packages.Load (import-alias /
// dot-import immune via TypesInfo.Uses), and the only sanctioned caller package
// is tools/packagesload. Upstream is a Go visibility ceiling: packages.Load is
// an exported func of an external module, so the language cannot forbid other
// packages from calling it — the depguard rule archtest-no-direct-packages-load
// already bars tools/archtest/*_test.go from importing it, and this archtest
// covers the rest of the tree.
//
// BLIND SPOTS (accepted Medium gaps; no current code uses these forms):
//  1. Func-value laundering — `f := packages.Load; f(cfg, …)` references the func
//     without a direct call selector and is NOT flagged. No loader does this.
//  2. Reflection-driven invocation. Not flagged.
//
// REVERSE SELF-CHECK: the scan asserts >= 1 packages.Load call was resolved AND
// found inside the packagesload funnel (positive control). If callee resolution
// silently broke, that count would be 0 and the test fails; if the funnel call
// were mis-classified as a violation, it would surface as a diagnostic.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sync"
	"testing"
)

const (
	xtoolsPackagesPath = "golang.org/x/tools/go/packages"
	// Derived from PlatformModulePath (ARCHTEST-MODULE-PATH-FUNNEL-01) — never a
	// bare "github.com/ghbvf/gocell" literal, so a module rename updates one place.
	packagesloadPkg  = PlatformModulePath + "/tools/packagesload"
	packagesLoadRule = "PACKAGES-LOAD-FUNNEL-01"
)

// TestPackagesLoadFunnel01 asserts no repo package other than tools/packagesload
// calls golang.org/x/tools/go/packages.Load directly.
func TestPackagesLoadFunnel01(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var (
		mu       sync.Mutex
		inFunnel int
	)
	diags := Run(t, Production(TypedOpts{Tests: true}), func(p *Pass) []Diagnostic {
		var d []Diagnostic
		pkgPath := p.Pkg.Path()
		sanctioned := pkgPath == packagesloadPkg
		for _, f := range p.Files {
			rel := p.Rel(f)
			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				if !isXToolsPackagesLoadCall(p.TypesInfo, call) {
					return
				}
				if sanctioned {
					mu.Lock()
					inFunnel++
					mu.Unlock()
					return
				}
				pos := p.Fset.Position(call.Pos())
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						packagesLoadRule+": %s:%d calls golang.org/x/tools/go/packages.Load "+
							"directly. Route the load through %s.Load(mode, cfg, …) with an "+
							"explicit Mode (ModeModule = GOWORK=off, or ModeWorkspace) so it does "+
							"not silently inherit the repo go.work.",
						rel, pos.Line, packagesloadPkg,
					),
				})
			})
		}
		return d
	})

	for _, diag := range diags {
		t.Errorf("%s", diag.Message)
	}
	if inFunnel < 1 {
		t.Fatalf("anti-vacuity: PACKAGES-LOAD-FUNNEL-01 resolved 0 packages.Load calls inside "+
			"%s; callee resolution may have silently broken.", packagesloadPkg)
	}
}

// isXToolsPackagesLoadCall reports whether call invokes the canonical
// golang.org/x/tools/go/packages.Load function. Uses go/types so import aliases
// and dot-imports cannot disguise the callee.
func isXToolsPackagesLoadCall(info *types.Info, call *ast.CallExpr) bool {
	if info == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok {
		return false
	}
	return fn.Pkg() != nil && fn.Pkg().Path() == xtoolsPackagesPath && fn.Name() == "Load"
}
