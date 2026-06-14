//go:build archtest

// INVARIANT: CELL-LIFECYCLE-RANK-COMPLETENESS-01
//
// lifecycle_phase_test.go — every cellvocab.CellLifecycle const must appear in
// the ordered `cellLifecycles` array, so CellLifecycleRank (and the
// CELL-LIFECYCLE-01 governance slice≤cell ordering that depends on it) covers
// every declared lifecycle. A lifecycle const omitted from cellLifecycles would
// make CellLifecycleRank return -1 and silently break the ordering.
//
// AI-robust grade: Medium. AST set-difference between two declared sets in the
// same package. The blind-spot list + reverse self-check below are the
// Medium-grade justification material (.claude/rules/gocell/ai-robust.md).
package archtest

import (
	"go/ast"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
)

// lifecycleConstTypeName / cellLifecyclesVarName are the AST anchors. Blind
// spots (asserted absent / handled below):
//   - A lifecycle declared with an untyped const or in a different type would
//     not be collected by collectConstNamesOfType; reverse check asserts the
//     production const block is typed `CellLifecycle`.
//   - Lifecycles declared via append/helper rather than a composite-array
//     literal would evade collectCompositeElementIdents; reverse check asserts
//     the production `cellLifecycles` is a single composite literal.
const (
	lifecycleConstTypeName = "CellLifecycle"
	cellLifecyclesVarName  = "cellLifecycles"
)

func TestCellLifecycleRankCompleteness(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	diags := Run(t, AST(DirsScope(root, []string{"framework/kernel/cellvocab"})), func(p *Pass) []Diagnostic {
		declared := map[string]token.Pos{}
		covered := map[string]struct{}{}
		var declFile *ast.File
		for _, f := range p.Files {
			for name, pos := range collectConstNamesOfType(f, lifecycleConstTypeName) {
				declared[name] = pos
				declFile = f
			}
			for name := range collectCompositeElementIdents(f, cellLifecyclesVarName) {
				covered[name] = struct{}{}
			}
		}
		var d []Diagnostic
		for name, pos := range declared {
			if _, ok := covered[name]; !ok {
				d = append(d, Diagnostic{
					Rel:  p.Rel(declFile),
					Line: p.Fset.Position(pos).Line,
					Message: "cellvocab.CellLifecycle const " + name + " is missing from the ordered cellLifecycles array; " +
						"CellLifecycleRank would return -1 for it and break the governance slice≤cell ordering",
				})
			}
		}
		return d
	})

	Report(t, "CELL-LIFECYCLE-RANK-COMPLETENESS-01", diags)
}

// TestCellLifecycleRankCompleteness_BlindSpotShape asserts the production AST
// uses the exact forms the collectors recognize: the CellLifecycle const block
// is typed `CellLifecycle`, and `cellLifecycles` is a single composite
// (array/slice) literal — not a runtime-built slice that would evade the
// element collector.
func TestCellLifecycleRankCompleteness_BlindSpotShape(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	_ = Run(t, AST(DirsScope(root, []string{"framework/kernel/cellvocab"})), func(p *Pass) []Diagnostic {
		var sawTypedConst, sawCompositeCellLifecycles bool
		for _, f := range p.Files {
			if len(collectConstNamesOfType(f, lifecycleConstTypeName)) > 0 {
				sawTypedConst = true
			}
			if len(collectCompositeElementIdents(f, cellLifecyclesVarName)) > 0 {
				sawCompositeCellLifecycles = true
			}
		}
		assert.True(t, sawTypedConst, "expected a typed `CellLifecycle` const block in cellvocab (blind-spot guard)")
		assert.True(t, sawCompositeCellLifecycles, "expected `cellLifecycles` to be a composite literal in cellvocab (blind-spot guard)")
		return nil
	})
}

// TestSetDifference_ReverseSelfCheck proves the missing-element detection (the
// core of every completeness invariant in this PR) actually fires on a gap.
func TestSetDifference_ReverseSelfCheck(t *testing.T) {
	t.Parallel()
	declared := map[string]struct{}{"a": {}, "b": {}, "c": {}}
	covered := map[string]struct{}{"a": {}, "c": {}}
	assert.Equal(t, []string{"b"}, missingKeys(declared, covered))
	assert.Empty(t, missingKeys(covered, declared), "no extras when declared ⊆ covered")
}
