package archtest

// projection_event_journal_append_caller.go — importable rule body for
// PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01 (EPIC #1504 PR-02 / ADR
// 202606071600-1504 §6 I2). The non-test home keeps the detector compilable so the
// _test.go companion (production dogfood + RED fixture) calls the same single
// source — no parallel rule body.
//
// # What this guards
//
// adapters/postgres.journalingOutboxWriter.appendProjectionEvents is the SOLE write
// path into the durable projection_events journal (the replay source for CQRS
// projections). If any other code could append, it could forge rows the projection
// harness will replay as truth — the #1504 "伪造 / 越界投影事件" threat. The journal
// has no exported append API (the source is read-only; conformance seeds via raw
// INSERT in _test.go), so this funnel locks the one internal append.
//
// # AI-robust rating — Hard/Hard fully-closed (charter §"Funnel 双向锁评级")
//
//   - Upstream Hard (Go visibility): appendProjectionEvents is UNEXPORTED, so no
//     package outside adapters/postgres can name it — a package-external append is
//     not expressible. This is a real seal, not a deferred TODO.
//   - Downstream Hard (whole-package caller-allowlist): upstream visibility only
//     bars package-EXTERNAL callers; a second function INSIDE adapters/postgres could
//     still call appendProjectionEvents and bypass the topic filter / tx binding.
//     This scanner resolves every reference to the method by go/types object
//     (info.Uses → *types.Func in adapters/postgres named appendProjectionEvents),
//     binds it to its enclosing FuncDecl identity (ResolveEnclosingFunc.FullName),
//     and allows exactly one caller: the chokepoint journalProjectionSubset. Any
//     other in-package caller fails CI. Together the two rungs are the
//     "Hard/Hard fully-closed" example from ai-robust.md §6 I2 — tighter than the
//     #851/#893/#1282 cross-package families whose append+caller straddle a package
//     boundary and are capped at Medium upstream by Go visibility.
//
// # Detection is REFERENCE-based and form-invariant
//
// Matching is by go/types object, so qualified, same-package-bare, aliased, and
// dot-import forms all resolve to the same *types.Func. The enclosing-caller bind
// is what makes it callsite-level (not file-level): all the decorator methods live
// in one file, so a file-level allowlist could not distinguish journalProjectionSubset
// from Write / appendProjectionEvents itself.
//
// # Blind spots and anti-vacuity
//
//   - A reference whose enclosing FuncDecl cannot be resolved (package-level
//     initializer) is a hard violation, not silently skipped.
//   - Reflection / unsafe construction is out of scope (charter §3) — same ceiling
//     as every identifier-resolution funnel.
//   - The anti-vacuity reverse check requires the chokepoint allowlist entry to have
//     ≥1 observed live reference, so a removed/renamed caller fails CI rather than
//     leaving a dead bypass slot.

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"
)

// journalAppendPkg is the canonical import path of adapters/postgres, anchored to
// PlatformModulePath so a module rename updates one place.
const journalAppendPkg = PlatformModulePath + "/adapters/postgres"

// journalAppendFunc is the unexported method name being locked.
const journalAppendFunc = "appendProjectionEvents"

// journalAppendCallerAllowlist is the set of caller identities (types.Func.FullName)
// permitted to reference appendProjectionEvents. Exactly one: the chokepoint
// journalProjectionSubset, through which both Write and WriteBatch funnel.
var journalAppendCallerAllowlist = map[string]struct{}{
	"(*" + journalAppendPkg + ".journalingOutboxWriter).journalProjectionSubset": {},
}

// CheckProjectionEventJournalAppendCaller01 runs the rule over the running module
// and returns its diagnostics (production dogfood single source).
func CheckProjectionEventJournalAppendCaller01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanJournalAppendCallers(p, journalAppendPkg, journalAppendFunc, journalAppendCallerAllowlist, observed)
	})
	diags = append(diags, staleJournalAppendAllowlistDiags(journalAppendCallerAllowlist, observed)...)
	return diags
}

// scanJournalAppendCallers records the enclosing-function identity of every
// reference to targetPkg.<targetFunc> into observed and returns a diagnostic for
// each reference whose enclosing caller is outside allowlist. Parameterized by
// (targetPkg, targetFunc, allowlist) so the same body drives the production scan
// and the RED fixture (the fixture reproduces the unexported-method shape under its
// own package path).
func scanJournalAppendCallers(
	p *Pass, targetPkg, targetFunc string, allowlist map[string]struct{}, observed map[string]struct{},
) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
			if id.Name != targetFunc {
				return
			}
			fn, ok := p.TypesInfo.Uses[id].(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg().Path() != targetPkg {
				return
			}
			line := p.Fset.Position(id.Pos()).Line
			caller, ok := ResolveEnclosingFunc(p.TypesInfo, file, id)
			if !ok {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: "PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01: " + targetFunc +
						" is referenced outside any FuncDecl (package-level initializer) — it " +
						"cannot be allowlisted at the callsite level. Append only from the " +
						"journalProjectionSubset chokepoint.",
				})
				return
			}
			callerID := caller.FullName()
			observed[callerID] = struct{}{}
			if _, allowed := allowlist[callerID]; allowed {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01: %s is referenced from caller %q, "+
						"which is not the sanctioned journal-append chokepoint. The durable "+
						"projection_events journal must have exactly one append path "+
						"(journalingOutboxWriter.journalProjectionSubset) so no code can forge rows "+
						"the projection harness replays as truth. If this IS a new sanctioned "+
						"append caller, add it to journalAppendCallerAllowlist with rationale.",
					targetFunc, callerID,
				),
			})
		})
	}
	return diags
}

// staleJournalAppendAllowlistDiags is the anti-vacuity reverse self-check: every
// allowlist entry must correspond to ≥1 live observed reference.
func staleJournalAppendAllowlistDiags(allowlist, observed map[string]struct{}) []Diagnostic {
	var diags []Diagnostic
	for caller := range allowlist {
		if _, seen := observed[caller]; seen {
			continue
		}
		diags = append(diags, Diagnostic{
			Message: fmt.Sprintf(
				"PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01: allowlist entry %q is STALE — no live "+
					"reference to %s observed from that caller. Either the scanner regressed or the "+
					"caller was removed/renamed; drop the dead entry so it cannot become a silent "+
					"bypass slot.",
				caller, journalAppendFunc,
			),
		})
	}
	return diags
}
