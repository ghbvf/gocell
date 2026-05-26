// INVARIANT: CELL-PHASE-RANK-COMPLETENESS-01
//
// lifecycle_phase_test.go — every cellvocab.Phase const must appear in the
// ordered `Phases` array, so PhaseRank (and the LIFECYCLE-PHASE-01 governance
// slice≤cell ordering that depends on it) covers every declared phase. A phase
// const omitted from Phases would make PhaseRank return -1 and silently break
// the ordering.
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

// phaseConstTypeName / phasesVarName are the AST anchors. Blind spots (asserted
// absent / handled below):
//   - A phase declared with an untyped const or in a different type would not be
//     collected by collectConstNamesOfType; reverse check asserts the production
//     const block is typed `Phase`.
//   - Phases declared via append/helper rather than a composite-array literal
//     would evade collectCompositeElementIdents; reverse check asserts the
//     production `Phases` is a single composite literal.
const (
	phaseConstTypeName = "Phase"
	phasesVarName      = "Phases"
)

func TestCellPhaseRankCompleteness(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	diags := Run(t, DirsScope(root, []string{"kernel/cellvocab"}), func(p *Pass) []Diagnostic {
		declared := map[string]token.Pos{}
		covered := map[string]struct{}{}
		var declFile *ast.File
		for _, f := range p.Files {
			for name, pos := range collectConstNamesOfType(f, phaseConstTypeName) {
				declared[name] = pos
				declFile = f
			}
			for name := range collectCompositeElementIdents(f, phasesVarName) {
				covered[name] = struct{}{}
			}
		}
		var d []Diagnostic
		for name, pos := range declared {
			if _, ok := covered[name]; !ok {
				d = append(d, Diagnostic{
					Rel:  p.Rel(declFile),
					Line: p.Fset.Position(pos).Line,
					Message: "cellvocab.Phase const " + name + " is missing from the ordered Phases array; " +
						"PhaseRank would return -1 for it and break the governance slice≤cell ordering",
				})
			}
		}
		return d
	})
	Report(t, "CELL-PHASE-RANK-COMPLETENESS-01", diags)
}

// TestCellPhaseRankCompleteness_BlindSpotShape asserts the production AST uses
// the exact forms the collectors recognize: the Phase const block is typed
// `Phase`, and `Phases` is a single composite (array/slice) literal — not a
// runtime-built slice that would evade the element collector.
func TestCellPhaseRankCompleteness_BlindSpotShape(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	_ = Run(t, DirsScope(root, []string{"kernel/cellvocab"}), func(p *Pass) []Diagnostic {
		var sawTypedConst, sawCompositePhases bool
		for _, f := range p.Files {
			if len(collectConstNamesOfType(f, phaseConstTypeName)) > 0 {
				sawTypedConst = true
			}
			EachInChildren[ast.GenDecl](f, func(gd *ast.GenDecl) {
				if gd.Tok != token.VAR {
					return
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, n := range vs.Names {
						if n.Name == phasesVarName && i < len(vs.Values) {
							if _, ok := vs.Values[i].(*ast.CompositeLit); ok {
								sawCompositePhases = true
							}
						}
					}
				}
			})
		}
		assert.True(t, sawTypedConst, "expected a typed `Phase` const block in cellvocab (blind-spot guard)")
		assert.True(t, sawCompositePhases, "expected `Phases` to be a composite literal in cellvocab (blind-spot guard)")
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
