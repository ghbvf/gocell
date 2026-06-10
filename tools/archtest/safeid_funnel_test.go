//go:build archtest

// safeid_funnel_test.go — dogfoods the SafeID wire-message funnel Check*
// functions against GoCell itself; detector logic lives in safeid_funnel.go
// (non-test) so external Cell repositories can compile and run it.
//
//   - INVARIANT: SAFEID-WIREMESSAGE-USAGE-01
//   - INVARIANT: SAFEID-UPSTREAM-FUNNEL-HARD-01
package archtest

import (
	"fmt"
	"go/ast"
	"testing"
)

// TestSAFEIDWireMessageUsage01 reflectively asserts that every exported
// field on wireMessage and ObservabilityMetadata is either typed
// idutil.SafeID or explicitly carved out in safeIDExemptFields. A new field
// default-fails until a reviewer either types it SafeID or registers a
// carve-out with rationale (deny-by-default).
func TestSAFEIDWireMessageUsage01(t *testing.T) {
	t.Parallel()
	Report(t, "SAFEID-WIREMESSAGE-USAGE-01",
		CheckSafeIDWireMessageUsage01(t, ConfigForExternalCell{}))
}

// TestSAFEIDWireMessageUsage01_BlindSpot_NewWireStruct asserts that no
// other struct in kernel/outbox has the same shape as wireMessage (i.e.
// id/eventType/aggregateId fields typed string) — covers the blind spot
// noted in the package godoc: a parallel wire struct that reintroduces
// untyped fields.
//
// Known limitation: this detector only checks canonical field names listed
// in safeIDWireFieldNames. A new wire-shape struct using alternative
// ID-shaped names (SourceID, CorrelationKey, SenderID) will not be detected.
// When adding such names to any wire struct, reviewers must extend
// safeIDWireFieldNames or directly carve out in safeIDExemptFields.
func TestSAFEIDWireMessageUsage01_BlindSpot_NewWireStruct(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./kernel/outbox/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != outboxPkgPath {
				return nil
			}
			diags = append(diags, checkSafeIDBlindSpotNewWireStruct(p.Pkg)...)
			return nil
		})

	Report(t, "SAFEID-WIREMESSAGE-USAGE-01/BlindSpot/NewWireStruct", diags)
}

// TestSAFEIDUpstreamFunnelHard01 asserts the upstream-side seal of the
// SafeID funnel: in kernel/outbox the wire envelope struct must be
// unexported (`wireMessage`), and no re-export under ANY name may
// expose its constructibility. Go package-level visibility then makes
// any cross-package construction or json.Unmarshal-decode-target syntax
// referencing the envelope a compile-time error — the upstream side of
// the funnel is enforced by the Go type system itself; this archtest is
// the regression guard against future re-exports.
//
// Six checks (all must pass):
//  1. `wireMessage` symbol exists in kernel/outbox package scope
//  2. The symbol is NOT exported (`Obj().Exported() == false`)
//  3. No exact-name `WireMessage` (exported) symbol exists in the same scope
//  4. `wireMessage`'s field set contains the canonical envelope fields
//     (defense in depth against a silent rename that drops SchemaVersion
//     or other load-bearing fields)
//  5. No exported alias of wireMessage exists under any name
//     (`type Envelope = wireMessage` — caught by Type() identity equality)
//  6. No exported struct with SchemaVersion + ≥7/10 canonical wireMessage
//     fields exists under any name (`type Envelope struct{...}` — re-shape
//     re-export under a fresh name; also flags `type Envelope wireMessage`
//     defined-type sharing the underlying struct identity)
func TestSAFEIDUpstreamFunnelHard01(t *testing.T) {
	t.Parallel()
	Report(t, "SAFEID-UPSTREAM-FUNNEL-HARD-01",
		CheckSafeIDUpstreamFunnelHard01(t, ConfigForExternalCell{}))
}

// TestSAFEIDUpstreamFunnelHard01_BlindSpot_NoReExport is the reverse
// self-test for SAFEID-UPSTREAM-FUNNEL-HARD-01: scan all non-test *.go
// files under kernel/outbox/ and assert no `type WireMessage <anything>`
// declaration exists (catches struct, alias, named type, interface, all
// forms). Redundant with the go/types Lookup("WireMessage") check in the
// main test, but provides defense-in-depth at the AST level so a future
// PR adding the literal token `type WireMessage` is caught even if the
// go/types loader misbehaves under build tags.
//
// Uses [Run] + [DirsScope] per ai-robust.md §"载体决策原则" — pure AST
// pattern, no type information required (looking for a literal `type
// WireMessage` token, not its resolved type). [DirsScope] applies the
// default file skip set (vendor / testdata / generated / worktrees /
// _test.go) so production *.go files are the only thing scanned.
func TestSAFEIDUpstreamFunnelHard01_BlindSpot_NoReExport(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"kernel/outbox"})

	var diags []Diagnostic
	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
				if ts.Name == nil || ts.Name.Name != wireMessageExportedOld {
					return
				}
				pos := p.Fset.Position(ts.Pos())
				diags = append(diags, Diagnostic{
					Message: fmt.Sprintf(
						"SAFEID-UPSTREAM-FUNNEL-HARD-01/NoReExport: %s:%d declares `type %s ...` — "+
							"the exported envelope must not be re-introduced; "+
							"all envelope I/O must go through outbox.MarshalEnvelope / "+
							"outbox.UnmarshalEnvelope with the unexported wireMessage",
						rel, pos.Line, wireMessageExportedOld,
					),
				})
			})
		}
		return nil
	})

	Report(t, "SAFEID-UPSTREAM-FUNNEL-HARD-01/BlindSpot/NoReExport", diags)
}
