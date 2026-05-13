// invariants asserted in this file:
//   - INVARIANT: PROM-CELL-LABEL-FUNNEL-01
//
// PROM-CELL-LABEL-FUNNEL-01: every read of cell.HookEvent.CellID in
// adapters/prometheus/*.go (non-test, excluding cell_label.go itself) must
// be the direct argument of a call to adapters/prometheus.promCellLabel(...).
// The single typed-function funnel routes the field through
// metadata.CellIDPattern validation; bypassing it would let a non-conforming
// cell_id reach Prometheus labels and create high-cardinality series
// silently.
//
// AI-rebust: Hard (typed-function-call funnel + types.Info form uniqueness,
// charter §4 Wave 2 same pattern as PANIC-REGISTERED-01). The funnel call
// is resolved via *types.Info so a same-name local variable or alias cannot
// bypass the check; the SelectorExpr's X is also type-checked to be
// kernel/cell.HookEvent (pointer or value) so unrelated *.CellID fields on
// future structs do not produce false positives.
//
// ref: adapters/prometheus/cell_label.go — promCellLabel funnel definition
// ref: tools/archtest/panic_invariants_test.go — companion Hard pattern
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
)

const (
	promAdapterPkgPath = "github.com/ghbvf/gocell/adapters/prometheus"
	promFunnelFuncName = "promCellLabel"
	cellPkgPath        = "github.com/ghbvf/gocell/kernel/cell"
	hookEventTypeName  = "HookEvent"
	cellLabelDefnFile  = "cell_label.go" // funnel definition; excluded from scan
	promRuleID         = "PROM-CELL-LABEL-FUNNEL-01"
)

// TestPromCellLabelFunnel enforces PROM-CELL-LABEL-FUNNEL-01.
func TestPromCellLabelFunnel(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	pkgs, errs, err := typeseval.LoadPackages(root, false, nil,
		"./adapters/prometheus/...")
	require.NoError(t, err, "LoadPackages failed")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	var violations []string

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		// Only scan the production package itself; skip the *_test variant
		// the test-loader synthesizes (its PkgPath ends with ".test" or has
		// the `_test` suffix).
		if p.PkgPath != promAdapterPkgPath {
			return
		}
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			rel, ok := fileroles.Rel(root, abs)
			if !ok {
				continue
			}
			// Skip the funnel definition file (it reads its own `id` parameter,
			// not a HookEvent.CellID) and any test file.
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if filepath.Base(rel) == cellLabelDefnFile {
				continue
			}

			violations = append(violations,
				scanPromCellLabelFunnel(p, file, rel)...)
		}
	})

	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("%s: cell.HookEvent.CellID reads in adapters/prometheus/ "+
			"must be the direct argument of promCellLabel(...).\n"+
			"Violations (%d):\n  %s",
			promRuleID, len(violations), strings.Join(violations, "\n  "))
	}
}

// scanPromCellLabelFunnel walks file's AST looking for cell.HookEvent.CellID
// reads, comparing each against the approved set (direct args of
// promCellLabel call). The approved set is computed by resolving every
// CallExpr's callee via *types.Info — string-name matching alone (e.g.
// `ident.Name == "promCellLabel"`) would let a same-name local variable
// bypass the funnel, which is why the Hard property requires type
// resolution (charter §1 form uniqueness).
func scanPromCellLabelFunnel(p *packages.Package, file *ast.File, rel string) []string {
	approved := approvedCellIDArgs(p.TypesInfo, file)

	var violations []string
	for sel := range cellIDSelectorExprs(p.TypesInfo, file) {
		if _, ok := approved[sel]; ok {
			continue
		}
		pos := p.Fset.Position(sel.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: cell.HookEvent.CellID read must be the direct argument of promCellLabel(...)",
			rel, pos.Line,
		))
	}
	return violations
}

// approvedCellIDArgs returns the set of *ast.SelectorExpr nodes that appear
// as the first positional argument of a call resolving to
// `adapters/prometheus.promCellLabel` (the funnel). These are the only
// approved cell.HookEvent.CellID reads under PROM-CELL-LABEL-FUNNEL-01.
//
// Callee resolution goes through *types.Info.Uses so a same-name local
// `promCellLabel` variable does NOT register as an approved call site.
func approvedCellIDArgs(info *types.Info, file *ast.File) map[*ast.SelectorExpr]struct{} {
	out := map[*ast.SelectorExpr]struct{}{}
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isPromFunnelCall(info, call.Fun) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		sel, ok := call.Args[0].(*ast.SelectorExpr)
		if !ok {
			return
		}
		out[sel] = struct{}{}
	})
	return out
}

// isPromFunnelCall resolves fun via *types.Info.Uses and reports whether
// it refers to adapters/prometheus.promCellLabel (the funnel). A same-name
// local variable or function declared in another package fails this check
// even though its AST form is identical — that is the Hard property.
func isPromFunnelCall(info *types.Info, fun ast.Expr) bool {
	if info == nil {
		return false
	}
	var ident *ast.Ident
	switch v := fun.(type) {
	case *ast.Ident:
		ident = v
	case *ast.SelectorExpr:
		ident = v.Sel
	default:
		return false
	}
	obj := info.Uses[ident]
	if obj == nil {
		return false
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == promAdapterPkgPath && fn.Name() == promFunnelFuncName
}

// cellIDSelectorExprs returns the set of *ast.SelectorExpr nodes that read
// the field `CellID` from a value (or pointer) of type kernel/cell.HookEvent.
// SelectorExpr nodes whose X is some unrelated struct that happens to have
// a CellID field do not appear in this set — that type discrimination is
// the precision guard against false positives.
func cellIDSelectorExprs(info *types.Info, file *ast.File) map[*ast.SelectorExpr]struct{} {
	out := map[*ast.SelectorExpr]struct{}{}
	if info == nil {
		return out
	}
	scanner.EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil || sel.Sel.Name != "CellID" {
			return
		}
		xType := info.TypeOf(sel.X)
		if xType == nil {
			return
		}
		if !isHookEventType(xType) {
			return
		}
		out[sel] = struct{}{}
	})
	return out
}

// isHookEventType reports whether t is kernel/cell.HookEvent (value or
// pointer). The check inspects the underlying *types.Named's object and
// package path so type aliases / renames in callers are tolerated as long
// as they resolve to the same defined type.
func isHookEventType(t types.Type) bool {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == cellPkgPath && obj.Name() == hookEventTypeName
}
