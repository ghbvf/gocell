//go:build archtest

// INVARIANT: PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01
//
// PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01 (EPIC #1504 PR-02 / ADR
// 202606071600-1504 §6 I5) — the journaling outbox writer decorator's projection-
// source topic set MUST be cellgen-derived, never a hand-typed literal.
//
// # What this guards
//
// journalingOutboxWriter double-writes only events whose topic is in projectionTopics
// (D4 topic-filter). If the composition root passed a hand-maintained []string literal,
// it would silently drift from the slice.yaml projection declarations — a new projection
// would not be journaled (rebuild gap) or a removed one would linger. The set is instead
// derived by cellgen into generatedProjectionSourceTopics() (modules_gen.go), so adding a
// projection auto-enrolls its topic on the next `gocell generate assembly`.
//
// # Two-axis AI-robust rating (charter §"Funnel 双向锁评级") — Hard
//
//   - Upstream Hard (codegen golden): generatedProjectionSourceTopics() is emitted by
//     kernel/assembly.GenerateModulesGen from slice.yaml contractUsages and byte-locked
//     by `gocell generate assembly --verify` (tools/generatedverify). Stale output —
//     metadata changed without regen — fails CI. The derivation correctness is covered by
//     TestCollectOutboxProjectionTopics.
//   - Downstream Hard (this archtest): every NewJournalingOutboxWriter callsite must pass
//     the generatedProjectionSourceTopics() accessor as the topic argument — resolved by
//     go/types object, so a composite literal, a different func, or a variable is flagged.
//     Without this rung, the golden-locked derivation could be bypassed by hand-typing the
//     argument at the callsite.
//
// # Blind spots and anti-vacuity
//
//   - A NewJournalingOutboxWriter call with <2 args cannot type-check, so the arg index is
//     always safe.
//   - The anti-vacuity guard requires ≥1 production NewJournalingOutboxWriter call to be
//     observed, so the rule cannot silently pass if the decorator is unwired.
//   - The RED fixture (projectiontopicfixture) is the negative control: a hand-typed []string
//     literal must be flagged.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	journalDecoratorPkg  = PlatformModulePath + "/adapters/postgres"
	journalDecoratorCtor = "NewJournalingOutboxWriter"
	// journalTopicsDerivedFunc is the cellgen-derived accessor the topic argument must be.
	journalTopicsDerivedFunc = "generatedProjectionSourceTopics"
)

// TestProjectionEventJournalTopicAllowlistDerived01 asserts that every production
// NewJournalingOutboxWriter callsite derives its topic set from
// generatedProjectionSourceTopics() (no hand-typed literal), and that at least one
// such call is observed (anti-vacuity — the decorator is wired).
func TestProjectionEventJournalTopicAllowlistDerived01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var observed int
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanJournalTopicDerivation(p, &observed)
	})
	if observed == 0 {
		diags = append(diags, Diagnostic{
			Message: "PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01: no production " +
				"NewJournalingOutboxWriter call observed — the journaling decorator appears unwired, " +
				"so the derived-topic-set rule is vacuous. Wire it in cmd/corebundle/cap_wiring.go.",
		})
	}
	Report(t, "PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01", diags)
}

// scanJournalTopicDerivation flags every NewJournalingOutboxWriter call whose topic
// argument is not the generatedProjectionSourceTopics() accessor, and counts the
// sanctioned calls into observed for the anti-vacuity check.
func scanJournalTopicDerivation(p *Pass, observed *int) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
			if !ok || pkgPath != journalDecoratorPkg || name != journalDecoratorCtor {
				return
			}
			if len(call.Args) < 2 {
				return // cannot type-check; the compiler rejects it first
			}
			if isDerivedTopicsArg(p.TypesInfo, call.Args[1]) {
				*observed++
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(call.Pos()).Line,
				Message: fmt.Sprintf(
					"PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01: %s is constructed with a "+
						"topic set that is not the cellgen-derived %s(). The projection-source topic "+
						"set must be derived from slice.yaml (so adding a projection auto-enrolls its "+
						"topic), never a hand-typed literal that silently drifts from metadata. Pass "+
						"generatedProjectionSourceTopics() as the topic argument.",
					journalDecoratorCtor, journalTopicsDerivedFunc,
				),
			})
		})
	}
	return diags
}

// isDerivedTopicsArg reports whether expr is a call to the generatedProjectionSourceTopics
// accessor (resolved by go/types object, so import shape is irrelevant).
func isDerivedTopicsArg(info *types.Info, expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	var id *ast.Ident
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		id = fun
	case *ast.SelectorExpr:
		id = fun.Sel
	default:
		return false
	}
	fn, ok := info.Uses[id].(*types.Func)
	return ok && fn.Name() == journalTopicsDerivedFunc
}

// TestProjectionEventJournalTopicAllowlistDerived01_RedFixture is the negative
// control: the detector must fire on a hand-typed []string topic literal.
func TestProjectionEventJournalTopicAllowlistDerived01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var throwaway int
	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/projectiontopicfixture"}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			found += len(scanJournalTopicDerivation(p, &throwaway))
			return nil
		})
	assert.Equal(t, 1, found,
		"PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01 RED fixture self-check FAILED: "+
			"expected exactly 1 violation (handTypedTopics passes a []string literal instead of "+
			"generatedProjectionSourceTopics()). Got %d.", found)
}
