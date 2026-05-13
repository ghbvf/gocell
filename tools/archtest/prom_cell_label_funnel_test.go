// invariants asserted in this file:
//   - INVARIANT: PROM-CELL-LABEL-FUNNEL-01
//
// PROM-CELL-LABEL-FUNNEL-01: every read of cell.HookEvent.CellID in
// adapters/prometheus/*.go (non-test, excluding cell_label.go itself) must
// be the direct argument of a call to promCellLabel(...). The single
// typed-function funnel routes the field through metadata.CellIDPattern
// validation; bypassing it would let a non-conforming cell_id reach
// Prometheus labels and create high-cardinality series silently.
//
// AI-rebust: Hard (typed-function-call funnel + AST form uniqueness,
// charter §4 Wave 2 same pattern as PANIC-REGISTERED-01).
//
// ref: adapters/prometheus/cell_label.go — promCellLabel funnel
// ref: tools/archtest/panic_invariants_test.go — companion Hard form pattern
package archtest

import (
	"go/ast"
	"go/parser"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestPromCellLabelFunnel enforces PROM-CELL-LABEL-FUNNEL-01.
func TestPromCellLabelFunnel(t *testing.T) {
	t.Parallel()

	repoRoot := repoRootFromTestPath(t)

	pred := scanner.MatchRels(func(rel string) bool {
		base := filepath.Base(rel)
		if strings.HasSuffix(base, "_test.go") {
			return false
		}
		// Exclude the funnel definition file itself — promCellLabel reads
		// the id string parameter; the rule applies to cell.HookEvent.CellID
		// field reads, which only occur in caller files.
		if base == "cell_label.go" {
			return false
		}
		return strings.HasSuffix(base, ".go")
	})

	scope := scanner.DirsScope(repoRoot, []string{
		"adapters/prometheus",
	}, pred)

	var violations []string

	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		// Build the set of *ast.SelectorExpr nodes that are direct argument
		// of a promCellLabel(...) call — these are the approved reads.
		approved := map[*ast.SelectorExpr]struct{}{}
		scanner.EachInSubtree[ast.CallExpr](fc.File, func(call *ast.CallExpr) {
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "promCellLabel" {
				return
			}
			if len(call.Args) == 0 {
				return
			}
			if sel, ok := call.Args[0].(*ast.SelectorExpr); ok {
				approved[sel] = struct{}{}
			}
		})

		// Walk every SelectorExpr; flag any `.CellID` read that is not in
		// the approved set.
		scanner.EachInSubtree[ast.SelectorExpr](fc.File, func(sel *ast.SelectorExpr) {
			if sel.Sel == nil || sel.Sel.Name != "CellID" {
				return
			}
			if _, ok := approved[sel]; ok {
				return
			}
			pos := fc.Fset.Position(sel.Pos())
			violations = append(violations,
				fc.Rel+":"+strconv.Itoa(pos.Line)+
					": direct .CellID access must route through promCellLabel(...)")
		})
	})

	if len(violations) > 0 {
		t.Fatalf("PROM-CELL-LABEL-FUNNEL-01: cell.HookEvent.CellID reads in adapters/prometheus/ "+
			"must be the direct argument of promCellLabel(...).\n"+
			"Violations (%d):\n  %s", len(violations), strings.Join(violations, "\n  "))
	}
}
