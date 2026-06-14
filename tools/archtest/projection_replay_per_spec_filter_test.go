//go:build archtest

// INVARIANT: PROJECTION-REPLAY-PER-SPEC-FILTER-01
//
// Package archtest enforces PROJECTION-REPLAY-PER-SPEC-FILTER-01: inside the
// kernel projection rebuild's single per-spec replay funnel `drainGap` (called by
// BOTH replayPhase for (0, head0] and catchupPhase for (head0, head1]), TWO shapes
// must hold:
//
//	(a) the business Apply reach (the applyOne call) MUST be guarded by the
//	    per-spec topic gate `entry.Stream() == c.spec.Topic`; and
//	(b) the foreign fall-through MUST call advanceOffsetPastForeign OUTSIDE that
//	    gate, so a foreign-stream entry advances the checkpoint without applying.
//
// A projection rebuild iterates the single whole-journal ReplaySource shared by
// every Coordinator, so without (a) a projection would apply foreign streams to
// its read model and the multi-projection fan-in (#1482, examples/todoorder
// orderprojection) would be unsound; without (b) a trailing foreign entry that
// lands during catchup would never advance the checkpoint and the bounded drain
// could never reach head1 — the #1574 C1 catchup-termination hang. Because both
// phases route through drainGap, guarding it covers replay AND catchup.
//
// Why this rule exists: the per-spec filter is a new FRAMEWORK invariant whose
// correctness the example's disjoint-sub-view fan-in (and rebuild termination)
// depends on. Per the AI-robust charter, a new constraint ships with a static
// guard (this archtest), a documented contract (drainGap/advanceOffsetPastForeign
// godoc + ADR amendment), and regression tests (TestRebuild_PerSpecTopicFilter +
// TestRebuild_ForeignDuringCatchup). Deleting the gate would silently reintroduce
// cross-stream contamination; deleting/mis-placing the foreign advance would
// reintroduce the catchup hang; this rule reds the moment either shape breaks.
//
// AI-robust: Medium (NOT a double-locked funnel — the gate is enforced by a
// single archtest, not by the type system).
//   - The enforcement is NAME-ANCHORED AST containment: the applyOne CallExpr
//     must sit inside the `entry.Stream() == c.spec.Topic` IfStmt body
//     (token.Pos containment + structural-name gate form). This mirrors the
//     Medium CHANGEPASSWORD-INACTIVE-GATE-01 dominance-lite shape — it is NOT
//     typed callsite-uniqueness (no TypesInfo.ObjectOf) and NOT control-flow
//     dominance, so it does not reach Hard.
//   - Note the trivial part that is NOT what this rule buys: business code
//     cannot reach the rebuild apply at all, but only because applyOne is a
//     private kernel method (ordinary Go visibility) — that is incidental, not
//     the invariant. The invariant this rule actually enforces is "the
//     FRAMEWORK's drainGap keeps the topic gate around applyOne and advances past
//     foreign streams outside it", and a name-anchored AST gate is a Medium guard
//     for it.
//   - Hard path (won't-do now): resolve applyOne via TypesInfo.ObjectOf to the
//     Coordinator method (typed callsite-uniqueness, à la
//     SAGA-STEP-RUN-OUTSIDE-TX-01 A1) AND verify CFG/SSA dominance of the gate
//     over the call. That is over-engineering for a single framework call site;
//     same ceiling family as #851 / #893 / #1282 / CHANGEPASSWORD #1212.
//
// Blind-spot inventory (tools: archtest.Run(t, Typed(...)) +
// scanner.EachInChildren/EachInSubtree + FindFirstInSubtree; name-anchored, not
// type-resolved, because the green/red fixtures use synthetic stand-in types so
// the detector must match by structural name, mirroring
// CHANGEPASSWORD-INACTIVE-GATE-01's mutation anchor):
//
//   - Anchor by name (drainGap / applyOne / advanceOffsetPastForeign /
//     Stream / spec.Topic): renaming drainGap makes the production scan fail
//     loudly (sawTarget=false → t.Fatal), not silently no-op. Renaming applyOne or
//     removing the gate trips sawApply / sawGate anti-vacuity fatals; removing the
//     foreign advance trips the sawForeignAdvance fatal. A rename of Stream
//     or the spec.Topic field would make isPerSpecTopicGate miss the gate →
//     sawGate fatal. Every drift direction is fail-closed.
//
//   - Cross-function extraction: moving the applyOne call into a helper called
//     from drainGap would drop the applyOne CallExpr from drainGap's subtree →
//     sawApply anti-vacuity fatal (the rule cannot silently pass). The helper
//     would then itself need its own gate review. This blind spot is
//     MACHINE-VERIFIED, not just documented: the crossfn fixture below extracts
//     applyOne into a helper, and the self-check asserts the detector reports
//     sawApply=false for it (the exact signal the production scan turns into a
//     t.Fatal).
//
//   - Enclosure is token.Pos containment within a topic-gate IfStmt.Body, not
//     full control-flow dominance: applyOne must sit inside a gate body, and
//     advanceOffsetPastForeign must sit outside every gate body (the foreign
//     fall-through). A gate whose body conditionally skips applyOne via a nested
//     branch is not modeled; that is a residual escape, fail-closed in the common
//     forms (an ungated applyOne / an in-gate foreign-advance are rejected). CFG
//     dominance is the Hard path (won't-do now, same ceiling family).
//
// Self-check: TestProjectionReplayPerSpecFilter_01_NegativeFixture loads the green
// (gated), red (ungated applyOne), crossfn (applyOne extracted to a helper), and
// foreigngate (advanceOffsetPastForeign nested inside the own gate) fixtures under
// tools/archtest/testdata/projection_replay_filter_{green,red,crossfn,foreigngate}
// and asserts the detector stays silent on green, fires on red + foreigngate, and
// reports
// sawApply=false on crossfn (the anti-vacuity signal).
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
	// psfTargetFunc is the framework function whose apply-gate + foreign-advance
	// placement is governed. It is the single per-spec replay funnel `drainGap` on
	// *Coordinator (replayPhase drains (0, head0], catchupPhase drains (head0,
	// head1] — both call drainGap, so guarding it covers BOTH phases). Matched by
	// name.
	psfTargetFunc = "drainGap"
	// psfApplyMethod is the business-apply reach within drainGap.
	psfApplyMethod = "applyOne"
	// psfForeignAdvanceMethod is the foreign-stream checkpoint-advance reach. The
	// foreign fall-through MUST call it (and OUTSIDE the own-topic gate) so a
	// trailing foreign entry advances the checkpoint and cannot stall catchup
	// (#1574 C1).
	psfForeignAdvanceMethod = "advanceOffsetPastForeign"
	// psfTopicAccessor / psfSpecField / psfSpecTopicField form the gate shape
	// `entry.Stream() == c.spec.Topic`. The accessor is Stream — the projection
	// carrier's routing-topic accessor on cellvocab.ProjectionEvent (EPIC #1609
	// PR-01 generalized the former concrete outbox.Entry accessor).
	psfTopicAccessor  = "Stream"
	psfSpecField      = "spec"
	psfSpecTopicField = "Topic"
)

// TestProjectionReplayPerSpecFilter_01 enforces that the production replayPhase
// calls applyOne only inside the per-spec topic gate.
func TestProjectionReplayPerSpecFilter_01(t *testing.T) {
	t.Parallel()

	var sawTarget, sawApply, sawGate, sawForeignAdvance bool
	diags := Run(t, Typed(TypedOpts{}, []string{"./framework/kernel/projection/..."}),
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
				fileDiags, found, apply, gate, fwd := perSpecFilterScan(p.Fset, file, rel)
				sawTarget = sawTarget || found
				sawApply = sawApply || apply
				sawGate = sawGate || gate
				sawForeignAdvance = sawForeignAdvance || fwd
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
	if !sawForeignAdvance {
		t.Fatalf("%s: production %q contains no %s(...) call — anti-vacuity: foreign streams MUST advance "+
			"the checkpoint or a trailing foreign entry stalls catchup (#1574 C1)",
			rulePerSpecReplayFilter01, psfTargetFunc, psfForeignAdvanceMethod)
	}

	Report(t, rulePerSpecReplayFilter01, diags)
}

// TestProjectionReplayPerSpecFilter_01_NegativeFixture verifies the detector
// stays silent on the gated (green) fixture, fires on the ungated (red) one, and
// reports sawApply=false on the crossfn fixture (applyOne extracted into a helper
// — the anti-vacuity signal that, in the production scan, is turned into a
// t.Fatal). This machine-verifies the cross-function-extraction blind spot.
func TestProjectionReplayPerSpecFilter_01_NegativeFixture(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path           string
		wantViolations bool
		wantSawApply   bool
	}{
		{path: "./tools/archtest/testdata/projection_replay_filter_green", wantViolations: false, wantSawApply: true},
		{path: "./tools/archtest/testdata/projection_replay_filter_red", wantViolations: true, wantSawApply: true},
		{path: "./tools/archtest/testdata/projection_replay_filter_crossfn", wantViolations: false, wantSawApply: false},
		// foreigngate: advanceOffsetPastForeign nested INSIDE the own-topic gate —
		// reverse self-check for the foreign-advance placement rule (#1574 F2). The
		// detector must fire ≥1 diagnostic; applyOne stays gated so sawApply=true.
		{path: "./tools/archtest/testdata/projection_replay_filter_foreigngate", wantViolations: true, wantSawApply: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			var sawTarget, sawApply bool
			diags := Run(t, Typed(TypedOpts{}, []string{tc.path}),
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
						fileDiags, found, apply, _, _ := perSpecFilterScan(p.Fset, file, rel)
						sawTarget = sawTarget || found
						sawApply = sawApply || apply
						d = append(d, fileDiags...)
					}
					return d
				})

			if !sawTarget {
				t.Fatalf("%s: fixture %q has no %s function — fixture broken",
					rulePerSpecReplayFilter01, tc.path, psfTargetFunc)
			}
			if sawApply != tc.wantSawApply {
				t.Errorf("%s: fixture %q sawApply = %v, want %v "+
					"(crossfn must report false — proves the detector sees no direct applyOne in drainGap)",
					rulePerSpecReplayFilter01, tc.path, sawApply, tc.wantSawApply)
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

// perSpecFilterScan finds the drainGap FuncDecl in file and reports a diagnostic
// for (a) each applyOne call NOT enclosed by a per-spec topic gate, and (b) each
// advanceOffsetPastForeign call that IS enclosed by the own-topic gate (the
// foreign fall-through must advance the checkpoint outside the gate). Returns
// whether the target function was present and whether any applyOne call / topic
// gate / foreign-advance call was seen (anti-vacuity signals for the caller).
func perSpecFilterScan(
	fset *token.FileSet, file *ast.File, rel string,
) (diags []Diagnostic, foundFunc, sawApply, sawGate, sawForeignAdvance bool) {
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

		// Every advanceOffsetPastForeign call must sit OUTSIDE the own-topic gate —
		// it is the foreign fall-through. A foreign-advance nested inside the own
		// gate (or absent — caught by the caller's sawForeignAdvance anti-vacuity)
		// would let a trailing foreign entry stall catchup (#1574 C1).
		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != psfForeignAdvanceMethod {
				return
			}
			sawForeignAdvance = true
			if psfPosWithinAny(call.Pos(), gateRanges) {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: fset.Position(call.Pos()).Line,
					Message: fmt.Sprintf(
						"%s: %s(...) in %s sits INSIDE the own-stream topic gate; the foreign "+
							"fall-through must advance the checkpoint OUTSIDE the gate so a trailing "+
							"foreign entry advances the checkpoint and cannot stall catchup (#1574 C1). "+
							"Move it to the non-gated path.",
						rulePerSpecReplayFilter01, psfForeignAdvanceMethod, psfTargetFunc),
				})
			}
		})
	})
	return diags, foundFunc, sawApply, sawGate, sawForeignAdvance
}

// isPerSpecTopicGate reports whether cond is the `<x>.Stream() == <y>.spec.Topic`
// equality gate (operand order independent).
func isPerSpecTopicGate(cond ast.Expr) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.EQL {
		return false
	}
	return psfHasStreamCall(bin) && psfHasSpecTopicSelector(bin)
}

// psfHasStreamCall reports whether n's subtree contains a `*.Stream()` call.
func psfHasStreamCall(n ast.Node) bool {
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
