package archtest

// invariants:
//   - INVARIANT: WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01
//
// # WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01 — cellmodules must not call bootstrap.WithManagedResource
//
// ## Rule
//
// No production file under `cellmodules/` may call
// `runtime/bootstrap.WithManagedResource(...)`. Cell modules declare their
// managed resources by returning them in `composition.ModuleResult.Resources`
// (the single-source channel introduced by PR #591 / #1420). The Builder
// (`runtime/composition/builder.go`) is the sole sanctioned caller that turns a
// module's `Resources` into `bootstrap.WithManagedResource(r)` AND into the
// pre-Run rollback stack — deriving BOTH from one source so the two can never
// diverge.
//
// Before #1420 each module wrote the same resource twice: once via
// `bootstrap.WithManagedResource(res)` in its opts (steady-state) and once in
// its provisional return (rollback). Forgetting either half leaked a resource or
// dropped a /readyz probe. Banning the call in cellmodules/ closes that
// double-write: a module CANNOT register a resource for the happy path except by
// listing it in `Resources`, which the Builder also rolls back.
//
// ## AI-robust rating
//
//   - Downstream: **Hard** — the funnel is closed by a zero-tolerance
//     caller-ban: `WithManagedResource` may not appear at any callsite under
//     cellmodules/. The qualifier is resolved to `runtime/bootstrap` via the
//     file's imports (so an import alias collapses to the same path and a decoy
//     `x.WithManagedResource` from another package does not match). Same shape
//     and tool as CONFIG-NOOP-TRANSFORMER-FUNNEL-01.
//   - Upstream: **Medium** — Go cannot make "call this exported func from
//     package X" inexpressible; a future cellmodules file CAN add the call and
//     only this archtest (CI) catches it. Same permanent ceiling as
//     MIGRATOR-PROVIDER-UP-CALLSITE-01 / CONFIG-NOOP-TRANSFORMER-FUNNEL-01 and
//     the holder-seal funnels (#851 / #893 / #1282).
//
// ## Blind spots (AST-only Run; documented per ai-robust §载体决策原则)
//
//   - `WithManagedResource` stored as a func value then called indirectly is not
//     tracked (no corpus case; the construction reference itself — the
//     SelectorExpr `bootstrap.WithManagedResource` — would still appear and is
//     out of scope of the CallExpr scan). A reverse self-check documents the
//     matched form.
//   - A new package added under cellmodules/ is auto-covered: the scope is the
//     `cellmodules/` path prefix, not an enumerated package list.
//   - Test files (_test.go) are excluded: builder/module unit tests legitimately
//     construct WithManagedResource to assemble fakes.
//   - Out-of-scope callers (cmd/corebundle, examples/iotdevice, examples/ssobff)
//     call bootstrap.New directly and are composition roots, not CellModules;
//     they live outside cellmodules/ and are never visited — the path-prefix
//     scope IS the allowlist boundary, so no per-caller carve-out is needed.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleWithManagedResourceCellmoduleFunnel01 = "WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01"
	// bootstrapPkgPath derived from PlatformModulePath per ARCHTEST-MODULE-PATH-FUNNEL-01.
	bootstrapPkgPath        = PlatformModulePath + "/runtime/bootstrap"
	withManagedResourceFunc = "WithManagedResource"
	cellmodulesScopePrefix  = "cellmodules/"
)

// TestWithManagedResourceCellmoduleFunnel01 enforces that no production file
// under cellmodules/ calls bootstrap.WithManagedResource — modules must return
// resources via ModuleResult.Resources and let the Builder funnel them.
func TestWithManagedResourceCellmoduleFunnel01(t *testing.T) {
	root := findModuleRoot(t)

	var violations []string
	Run(t, AST(ModuleScope(root)), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			rel := p.Rel(file)
			if !strings.HasPrefix(rel, cellmodulesScopePrefix) {
				continue
			}
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != withManagedResourceFunc {
					return
				}
				qual, isIdent := sel.X.(*ast.Ident)
				if !isIdent || importPathForQualifier(file, qual.Name) != bootstrapPkgPath {
					return
				}
				fnName := enclosingFuncName(file, call.Pos())
				if fnName == "" {
					fnName = "<package-level>"
				}
				violations = append(violations, rel+"::"+fnName)
			})
		}
		return nil
	})

	assert.Emptyf(t, violations,
		"%s: bootstrap.WithManagedResource called inside cellmodules/ (offending sites: %v). "+
			"Cell modules must declare resources via composition.ModuleResult.Resources; the "+
			"Builder is the sole funnel that calls WithManagedResource (and derives rollback from "+
			"the same source). Move the resource into the returned ModuleResult.Resources slice.",
		ruleWithManagedResourceCellmoduleFunnel01, violations)
}

// TestWithManagedResourceCellmoduleFunnel01_DetectsViolation is the reverse
// self-check: it proves the scan's predicate flags a bootstrap.WithManagedResource
// call and does NOT mis-flag a same-named call from a different (non-bootstrap)
// package. Without this, a regression that silently stopped matching would pass
// the forward test vacuously.
func TestWithManagedResourceCellmoduleFunnel01_DetectsViolation(t *testing.T) {
	const src = `package foo

import (
	bootstrap "github.com/ghbvf/gocell/runtime/bootstrap"
	other "example.com/other"
)

func bad() any { return bootstrap.WithManagedResource(nil) }

func decoy() any { return other.WithManagedResource(nil) }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "module.go", src, 0)
	require.NoError(t, err)

	var bootstrapHits, decoyHits []string
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != withManagedResourceFunc {
			return
		}
		qual, isIdent := sel.X.(*ast.Ident)
		if !isIdent {
			return
		}
		fnName := enclosingFuncName(file, call.Pos())
		if importPathForQualifier(file, qual.Name) == bootstrapPkgPath {
			bootstrapHits = append(bootstrapHits, fnName)
		} else {
			decoyHits = append(decoyHits, fnName)
		}
	})

	assert.Equal(t, []string{"bad"}, bootstrapHits,
		"the bootstrap.WithManagedResource call in bad() must be detected")
	assert.Equal(t, []string{"decoy"}, decoyHits,
		"a same-named call from a non-bootstrap package must NOT resolve to runtime/bootstrap")
}
