// INVARIANT: WEBHOOK-IDEMPOTENCY-CLAIMER-01
//
// This file owns ONE invariant: in runtime/webhook, every constructed `claimed`
// token whose `rcpt` field is set MUST source that receipt from a real
// idempotency.Claimer.Claim(...) call — the receipt's provenance is the Claimer,
// never a fabricated / zero / NonAcquiredReceipt value.
//
// # Why this is NOT redundant with WEBHOOK-RECEIVER-PIPELINE-01
//
// PIPELINE-01 seals ORDERING: a `claimed` token can only be constructed inside
// the function `claim`, and the business handler can only be invoked with a
// `claimed` token (verify → claim → invokeHandler). It does NOT prove that the
// `claim` step does real idempotency work — a refactor could build
// `claimed{rcpt: someZeroReceipt}` inside `claim` and still pass PIPELINE-01,
// silently disabling deduplication while keeping the receive pipeline's shape.
// CLAIMER-01 locks SUBSTANCE: the rcpt field must data-flow from Claimer.Claim.
// The two invariants are orthogonal axes (ordering vs. provenance).
//
// # Mechanism (type-keyed data-flow, rename-proof)
//
// A1: build the set of var objects that are bound, anywhere in the package, by an
// assignment whose sole RHS is a CallExpr resolving (via go/types) to the method
// idempotency.Claimer.Claim — `state, rcpt, err := r.claimer.Claim(...)` puts the
// `rcpt` object into the set. Then, for every CompositeLit whose declared type is
// the package's sealed `claimed` named type (types.Identical, not name-prefix),
// require the `rcpt` field's value (when present) to be an Ident whose object is
// in that set. The empty `claimed{}` literal (the error-path zero token, which is
// never fed to the handler) has no rcpt field and is therefore exempt.
//
// The check keys on the `claimed` TYPE identity + the Claimer.Claim METHOD
// identity + Go object identity for the rcpt var — NOT on the function name
// `claim`. Renaming `claim` to anything does not escape it (unlike a name-keyed
// "function claim must call Claim" check, which would be a Soft, rename-escapable
// convention and is deliberately NOT what this archtest does).
//
// # AI-robust rating
//
// Single-axis Medium (NOT a funnel — there is no caller-allowlist downstream /
// sealed-interface upstream to split, so the two-column funnel grading does not
// apply; same shape as SAGA-CONSTRUCTOR-NIL-GUARD-01). Downstream: archtest
// type-aware data-flow (object identity + method resolution); a fabricated rcpt
// is detectable but Go cannot make it a compile error. Upstream: Go cannot force
// a function to call a method — the permanent ceiling shared with
// WEBHOOK-RECEIVER-PIPELINE-01 upstream and the gh #1282 family (won't-do).
//
// # Blind spots (per ai-robust.md §"工具选定后强制盲区自检")
//
//   - Intra-function reflect/aliasing: a receipt laundered through reflect or an
//     interface round-trip before reaching the rcpt field is not traced. Bounded:
//     reflect on idempotency.Receipt is not expressible at the AST layer and is
//     not used anywhere in runtime/webhook; the single production construction
//     site (receiver.go claim) is the GREEN baseline.
//   - rcpt sourced from a Claim call whose result is reassigned through an
//     intermediate var chain (rcpt2 := rcpt; claimed{rcpt: rcpt2}) is not traced
//     past one hop. Bounded: the production form is the direct multi-assign; a
//     chain would be visually distinct and caught in review.
//
// # Reverse self-check (non-vacuous proof)
//
// TestWebhookIdempotencyClaimer01_Fixture runs the scanner on a fixture whose
// claim fabricates the receipt (no Claimer.Claim) and asserts it is flagged; the
// production runtime/webhook Pass is the GREEN baseline (0 diagnostics).
package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

const idempotencyClaimerPkgPath = PlatformModulePath + "/kernel/idempotency"

// namedName unwraps pointers and returns the named type's name, or "".
func namedName(t types.Type) string {
	for {
		switch tt := t.(type) {
		case *types.Pointer:
			t = tt.Elem()
		case *types.Named:
			if tt.Obj() != nil {
				return tt.Obj().Name()
			}
			return ""
		default:
			return ""
		}
	}
}

// isClaimerClaimCall reports whether call resolves to idempotency.Claimer.Claim.
func isClaimerClaimCall(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Claim" {
		return false
	}
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil || fn.Name() != "Claim" || fn.Pkg() == nil {
		return false
	}
	if fn.Pkg().Path() != idempotencyClaimerPkgPath {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	return namedName(sig.Recv().Type()) == "Claimer"
}

// scanClaimerProvenance implements WEBHOOK-IDEMPOTENCY-CLAIMER-01/A1: every
// `claimed{... rcpt: X ...}` literal in the scanned package must have X be an
// Ident bound from an idempotency.Claimer.Claim call. The scanner detects the
// package's own `claimed` type by name, so it serves both the production Pass
// (runtime/webhook) and the reverse fixture Pass.
func scanClaimerProvenance(p *Pass) []Diagnostic {
	info := p.TypesInfo
	if info == nil || p.Pkg == nil {
		return nil
	}
	tn, ok := p.Pkg.Scope().Lookup("claimed").(*types.TypeName)
	if !ok {
		return nil
	}
	claimedType := tn.Type()

	// Pass 1: collect var objects sourced from a Claimer.Claim call.
	claimSourced := map[types.Object]bool{}
	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
			if len(as.Rhs) != 1 {
				return
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok || !isClaimerClaimCall(info, call) {
				return
			}
			for _, lhs := range as.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					if obj := info.ObjectOf(id); obj != nil {
						claimSourced[obj] = true
					}
				}
			}
		})
	}

	// Pass 2: every claimed{...rcpt:...} literal must source rcpt from the set.
	var diags []Diagnostic
	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		rel := p.Rel(file)
		EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
			if cl.Type == nil {
				return
			}
			t := info.TypeOf(cl.Type)
			if t == nil || !types.Identical(t, claimedType) {
				return
			}
			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "rcpt" {
					continue
				}
				valIdent, ok := kv.Value.(*ast.Ident)
				if ok && claimSourced[info.ObjectOf(valIdent)] {
					continue // provenance confirmed
				}
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(kv.Value.Pos()).Line,
					Message: "claimed.rcpt is not sourced from idempotency.Claimer.Claim — the receipt " +
						"must data-flow from a `... := r.claimer.Claim(...)` call, not a fabricated / " +
						"zero / NonAcquiredReceipt value (WEBHOOK-IDEMPOTENCY-CLAIMER-01/A1)",
				})
			}
		})
	}
	return diags
}

// TestWebhookIdempotencyClaimer01 is the production GREEN baseline: the single
// claimed{rcpt: rcpt} construction in runtime/webhook sources rcpt from
// r.claimer.Claim, so the scanner reports nothing.
func TestWebhookIdempotencyClaimer01(t *testing.T) {
	t.Parallel()

	const runtimeWebhookPkg = PlatformModulePath + "/runtime/webhook"
	var allDiags []Diagnostic
	sawClaimed := false
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != runtimeWebhookPkg {
			return nil
		}
		if _, ok := p.Pkg.Scope().Lookup("claimed").(*types.TypeName); ok {
			sawClaimed = true
		}
		allDiags = append(allDiags, scanClaimerProvenance(p)...)
		return nil
	})

	if !sawClaimed {
		t.Fatal("WEBHOOK-IDEMPOTENCY-CLAIMER-01: `claimed` type not found in runtime/webhook — " +
			"renamed or removed? The scanner cannot anchor without it.")
	}
	Report(t, "WEBHOOK-IDEMPOTENCY-CLAIMER-01", allDiags)
}

// TestWebhookIdempotencyClaimer01_Fixture proves the scanner is non-vacuous: a
// claim that fabricates the receipt (no Claimer.Claim) is flagged.
func TestWebhookIdempotencyClaimer01_Fixture(t *testing.T) {
	t.Parallel()
	pattern := "./tools/archtest/testdata/webhook_claimer_violate"
	diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), scanClaimerProvenance)
	if len(diags) != 1 {
		t.Fatalf("WEBHOOK-IDEMPOTENCY-CLAIMER-01 reverse fixture: want exactly 1 diagnostic "+
			"(the fabricated-receipt claim), got %d: %v", len(diags), diags)
	}
}
