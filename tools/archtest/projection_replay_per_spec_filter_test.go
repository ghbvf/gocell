// INVARIANT: PROJECTION-REPLAY-PER-SPEC-FILTER-01
//
// Package archtest enforces PROJECTION-REPLAY-PER-SPEC-FILTER-01: inside the
// kernel projection rebuild, the business Apply reach (the applyOne call in
// replayPhase) MUST be guarded by the per-spec topic gate
// `entry.RoutingTopic() == c.spec.Topic`. A projection rebuild iterates the
// single whole-journal ReplaySource shared by every Coordinator, so without the
// gate a projection would apply foreign streams to its read model and the
// multi-projection fan-in (#1482, examples/todoorder orderprojection) would be
// unsound. Foreign streams must instead route through advanceOffsetPastForeign
// (advances the checkpoint without applying).
//
// Why this rule exists: the per-spec filter is a new FRAMEWORK invariant whose
// correctness the example's disjoint-sub-view fan-in depends on. Per the
// AI-robust charter, a new constraint ships with a static guard (this archtest),
// a documented contract (replayPhase godoc + ADR amendment), and a regression
// test (TestRebuild_PerSpecTopicFilter). Deleting the gate would silently
// reintroduce cross-stream contamination during rebuild; this rule reds the
// moment the gate is removed or the applyOne call escapes it.
//
// AI-robust: downstream Hard / upstream Medium (the #851 / #893 / #1282
// framework-owned ceiling family).
//   - Downstream Hard: the framework owns the rebuild replay loop; business
//     code has NO path to apply a foreign-stream entry during rebuild because
//     the applyOne call is structurally enclosed by the topic gate and the
//     gate form is asserted here. The only apply reach from replayPhase is the
//     gated one.
//   - Upstream Medium: this archtest asserts the gate's PRESENCE + the applyOne
//     callsite's enclosure, but Go cannot compile-time force replayPhase to keep
//     the gate. A token.Pos enclosure + AST form check is the Go-reachable
//     ceiling for "this call sits behind that condition"; full CFG/SSA dominance
//     is the path to Hard and is over-engineering for a single framework call
//     site (analogous to CHANGEPASSWORD-INACTIVE-GATE-01's #1212 note).
//
// Blind-spot inventory (tools: archtest.Run(t, Typed(...)) +
// scanner.EachInChildren/EachInSubtree + FindFirstInSubtree; name-anchored, not
// type-resolved, because the green/red fixtures use synthetic stand-in types so
// the detector must match by structural name, mirroring
// CHANGEPASSWORD-INACTIVE-GATE-01's mutation anchor):
//
//   - Anchor by name (replayPhase / applyOne / RoutingTopic / spec.Topic):
//     renaming replayPhase makes the production scan fail loudly (sawTarget=false
//     → t.Fatal), not silently no-op. Renaming applyOne or removing the gate
//     trips sawApply / sawGate anti-vacuity fatals. A rename of RoutingTopic or
//     the spec.Topic field would make isPerSpecTopicGate miss the gate → sawGate
//     fatal. Every drift direction is fail-closed.
//
//   - Cross-function extraction: moving the applyOne call into a helper called
//     from replayPhase would drop the applyOne CallExpr from replayPhase's
//     subtree → sawApply anti-vacuity fatal (the rule cannot silently pass). The
//     helper would then itself need its own gate review.
//
//   - Enclosure is token.Pos containment of the applyOne call within a
//     topic-gate IfStmt.Body, not full control-flow dominance. A gate whose
//     body conditionally skips applyOne via a nested branch is not modeled;
//     that is a residual escape, fail-closed in the common forms (an ungated
//     applyOne is rejected). CFG dominance is the Hard path (won't-do now, same
//     ceiling family).
//
// Self-check: TestProjectionReplayPerSpecFilter_01_NegativeFixture loads the
// green (gated) and red (ungated) fixtures under
// tools/archtest/testdata/projection_replay_filter_{green,red} and asserts the
// detector stays silent on green and fires on red.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
	"testing"
)

const (
	rulePerSpecReplayFilter01 = "PROJECTION-REPLAY-PER-SPEC-FILTER-01"
	// psfTargetFunc is the framework function whose apply-gate placement is
	// governed. It is a method on *Coordinator in production; matched by name.
	psfTargetFunc = "replayPhase"
	// psfApplyMethod is the business-apply reach within replayPhase.
	psfApplyMethod = "applyOne"
	// psfTopicAccessor / psfSpecField / psfSpecTopicField form the gate shape
	// `entry.RoutingTopic() == c.spec.Topic`.
	psfTopicAccessor  = "RoutingTopic"
	psfSpecField      = "spec"
	psfSpecTopicField = "Topic"
)

// TestProjectionReplayPerSpecFilter_01 enforces that the production replayPhase
// calls applyOne only inside the per-spec topic gate.
func TestProjectionReplayPerSpecFilter_01(t *testing.T) {
	t.Parallel()

	var sawTarget, sawApply, sawGate bool
	diags := Run(t, Typed(TypedOpts{}, []string{"./kernel/projection/..."}),
		func(p *Pass) []Diagnostic {
			if p.Fset == nil {
				return nil
			}
			var d []Diagnostic
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				fileDiags, found, apply, gate := perSpecFilterScan(p.Fset, file, rel)
				sawTarget = sawTarget || found
				sawApply = sawApply || apply
				sawGate = sawGate || gate
				d = append(d, fileDiags...)
			}
			return d
		})

	if !sawTarget {
		t.Fatalf("%s: production %q not found under kernel/projection/ — renamed or removed? "+
			"the per-spec replay-filter gate check must not be a silent no-op",
			rulePerSpecReplayFilter01, psfTargetFunc)
	}
	if !sawApply {
		t.Fatalf("%s: production %q contains no %s(...) call — anti-vacuity: the rule must guard a real apply reach",
			rulePerSpecReplayFilter01, psfTargetFunc, psfApplyMethod)
	}
	if !sawGate {
		t.Fatalf("%s: production %q contains no `%s() == c.%s.%s` gate — the per-spec filter was removed",
			rulePerSpecReplayFilter01, psfTargetFunc, psfTopicAccessor, psfSpecField, psfSpecTopicField)
	}

	Report(t, rulePerSpecReplayFilter01, diags)
}

// TestProjectionReplayPerSpecFilter_01_NegativeFixture verifies the detector
// stays silent on the gated (green) fixture and fires on the ungated (red) one.
func TestProjectionReplayPerSpecFilter_01_NegativeFixture(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path           string
		wantViolations bool
	}{
		{path: "./tools/archtest/testdata/projection_replay_filter_green", wantViolations: false},
		{path: "./tools/archtest/testdata/projection_replay_filter_red", wantViolations: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			var sawTarget bool
			diags := Run(t, Typed(TypedOpts{}, []string{tc.path}),
				func(p *Pass) []Diagnostic {
					if p.Fset == nil {
						return nil
					}
					var d []Diagnostic
					for _, file := range p.Files {
						rel := p.Rel(file)
						fileDiags, found, _, _ := perSpecFilterScan(p.Fset, file, rel)
						sawTarget = sawTarget || found
						d = append(d, fileDiags...)
					}
					return d
				})

			if !sawTarget {
				t.Fatalf("%s: fixture %q has no %s function — fixture broken",
					rulePerSpecReplayFilter01, tc.path, psfTargetFunc)
			}
			if tc.wantViolations && len(diags) == 0 {
				t.Errorf("%s: fixture %q expected ≥1 diagnostic, got 0 — detector broken",
					rulePerSpecReplayFilter01, tc.path)
			}
			if !tc.wantViolations && len(diags) > 0 {
				for _, d := range diags {
					t.Errorf("  %s:%d: %s", d.Rel, d.Line, d.Message)
				}
				t.Errorf("%s: fixture %q expected 0 diagnostics, got %d",
					rulePerSpecReplayFilter01, tc.path, len(diags))
			}
		})
	}
}

// psfPosRange is a half-open [lo, hi) token.Pos interval (an IfStmt.Body span).
type psfPosRange struct {
	lo, hi token.Pos
}

// perSpecFilterScan finds the replayPhase FuncDecl in file and reports a
// diagnostic for each applyOne call not enclosed by a per-spec topic gate.
// Returns whether the target function was present and whether any applyOne call
// / topic gate was seen (anti-vacuity signals for the caller).
func perSpecFilterScan(fset *token.FileSet, file *ast.File, rel string) (diags []Diagnostic, foundFunc, sawApply, sawGate bool) {
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil || fd.Name.Name != psfTargetFunc || fd.Body == nil {
			return
		}
		foundFunc = true

		// Collect the body spans of every per-spec topic gate in the function
		// subtree (descends into the nested Replay/RunInTx FuncLits).
		var gateRanges []psfPosRange
		EachInSubtree[ast.IfStmt](fd.Body, func(ifs *ast.IfStmt) {
			if isPerSpecTopicGate(ifs.Cond) {
				sawGate = true
				gateRanges = append(gateRanges, psfPosRange{lo: ifs.Body.Pos(), hi: ifs.Body.End()})
			}
		})

		// Every applyOne call must sit inside a gate body.
		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != psfApplyMethod {
				return
			}
			sawApply = true
			if !psfPosWithinAny(call.Pos(), gateRanges) {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: fset.Position(call.Pos()).Line,
					Message: fmt.Sprintf(
						"%s: %s(...) in %s is not guarded by the per-spec topic gate "+
							"`entry.%s() == c.%s.%s`; during a rebuild the business Apply must run "+
							"only for the projection's own stream (#1482). Wrap the applyOne call in "+
							"the topic gate; route foreign streams through advanceOffsetPastForeign.",
						rulePerSpecReplayFilter01, psfApplyMethod, psfTargetFunc,
						psfTopicAccessor, psfSpecField, psfSpecTopicField),
				})
			}
		})
	})
	return diags, foundFunc, sawApply, sawGate
}

// isPerSpecTopicGate reports whether cond is the `<x>.RoutingTopic() == <y>.spec.Topic`
// equality gate (operand order independent).
func isPerSpecTopicGate(cond ast.Expr) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.EQL {
		return false
	}
	return psfHasRoutingTopicCall(bin) && psfHasSpecTopicSelector(bin)
}

// psfHasRoutingTopicCall reports whether n's subtree contains a `*.RoutingTopic()` call.
func psfHasRoutingTopicCall(n ast.Node) bool {
	_, found := FindFirstInSubtree[ast.CallExpr](n, func(call *ast.CallExpr) bool {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel != nil && sel.Sel.Name == psfTopicAccessor
	})
	return found
}

// psfHasSpecTopicSelector reports whether n's subtree contains a `*.spec.Topic` selector.
func psfHasSpecTopicSelector(n ast.Node) bool {
	_, found := FindFirstInSubtree[ast.SelectorExpr](n, func(sel *ast.SelectorExpr) bool {
		if sel.Sel == nil || sel.Sel.Name != psfSpecTopicField {
			return false
		}
		inner, ok := sel.X.(*ast.SelectorExpr)
		return ok && inner.Sel != nil && inner.Sel.Name == psfSpecField
	})
	return found
}

// psfPosWithinAny reports whether p falls inside any half-open [lo, hi) range.
func psfPosWithinAny(p token.Pos, ranges []psfPosRange) bool {
	for _, r := range ranges {
		if p >= r.lo && p < r.hi {
			return true
		}
	}
	return false
}
