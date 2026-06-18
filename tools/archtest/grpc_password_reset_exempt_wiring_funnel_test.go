//go:build archtest

// Package archtest — grpc_password_reset_exempt_wiring_funnel_test.go
//
// INVARIANT: GRPC-PASSWORD-RESET-EXEMPT-WIRING-FUNNEL-01
//
// The runtime registrar is the SINGLE source of the gRPC password-reset-exempt set
// (#1382). The only sanctioned production installer of the auth interceptor's
// WithPasswordResetExempt predicate is authChainOptions in
// runtime/grpc/interceptor/chain.go, which wires
// WithPasswordResetExempt(reg.IsPasswordResetExemptMethod) for both the unary and
// stream chains (stream.go routes through that helper, so it holds no direct
// reference). Password-reset-exempt methods are declared in the contract via
// endpoints.grpc.methods[] (passwordResetExempt:true), derived by cellgen into
// GRPCServiceSpec.PasswordResetExemptMethods, and aggregated by the registrar.
//
// This archtest locks the runtime single-source in TWO dimensions:
//
//   - Dimension 1 (API ref): forbids any production reference to
//     interceptor.WithPasswordResetExempt outside chain.go.
//     WithPasswordResetExempt predicates compose (OR): a method is exempt if ANY
//     installed predicate returns true, so a composition root passing its own
//     WithPasswordResetExempt via Deps.AuthOptions would WIDEN the exempt set beyond
//     the contract-derived overlay — a latent auth bypass. Allowlisting only chain.go
//     makes the composed union a single member (the registrar) in production.
//   - Dimension 2 (field write): forbids any production WRITE of the unexported
//     authConfig.passwordResetExempt field outside auth.go (#1382 review F1).
//     Dimension 1 alone misses a same-package AuthOption that sets
//     c.passwordResetExempt directly without referencing WithPasswordResetExempt;
//     locking the actual state slot closes that bypass. Only
//     WithPasswordResetExempt (auth.go) may write it.
//
// Together they make the registrar the provably-sole production source. This is the
// runtime-side sibling of the codegen-side locks (cellgen golden + contractgen
// overlay referential pre-pass + governance FMT-41) and mirrors
// GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01.
//
// # AI-robust rating (per .claude/rules/gocell/ai-robust.md)
//
// Medium — a go/types caller-allowlist typed scan, same tier and mechanism as
// GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01. Hard-downstream is not reachable:
// WithPasswordResetExempt is an exported function; the single-source guarantee is the
// append-last wiring (chain.go) plus this allowlist, not type sealing. Upstream
// (contract → registrar) is locked by the codegen golden + contractgen pre-pass.
//
// # Blind spots (per AI-robust §"强制盲区自检")
//
//   - A caller that obtains WithPasswordResetExempt through a variable/parameter
//     typed as AuthOption (not a direct reference to the func object) escapes the
//     ident scan — the same alias blind spot GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01
//     documents. The value-ref case (f := WithPasswordResetExempt; opt := f(...)) IS
//     caught: the RHS ident resolves to the func object via go/types Uses.
//   - The scan is production-only (Production excludes _test.go); tests freely use
//     WithPasswordResetExempt to exercise the predicate.
//
// Anti-vacuity: the sole allowlisted file must host a live reference (stale-entry
// reverse check below), and
// TestArchtest_GRPCPasswordResetExemptWiringFunnel01_NegativeControl runs the
// identical scan with an EMPTY allowlist and asserts the live chain.go reference IS
// flagged — proving the matcher is not vacuously green.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// passwordResetExemptFieldWriteAllowlist is the set of production files permitted
// to WRITE the unexported authConfig.passwordResetExempt field — the gRPC
// password-reset-exempt state slot. The sole sanctioned writer is
// WithPasswordResetExempt in auth.go. Locking the FIELD (not just the
// WithPasswordResetExempt API reference) closes the same-package bypass where a new
// in-package AuthOption assigns c.passwordResetExempt directly, widening the exempt
// set without going through WithPasswordResetExempt / the registrar single source
// (#1382 review F1).
var passwordResetExemptFieldWriteAllowlist = map[string]struct{}{
	"runtime/grpc/interceptor/auth.go": {},
}

// isAuthConfigPasswordResetExemptField reports whether obj is the unexported
// authConfig.passwordResetExempt field. The field name is unique within the
// interceptor package, so name + IsField + package is precise.
func isAuthConfigPasswordResetExemptField(obj types.Object) bool {
	v, ok := obj.(*types.Var)
	return ok && v.IsField() && v.Name() == "passwordResetExempt" &&
		v.Pkg() != nil && v.Pkg().Path() == grpcInterceptorPkgPath
}

// scanPasswordResetExemptFieldWrites scans production code for WRITES to
// authConfig.passwordResetExempt — assignment LHS (c.passwordResetExempt = ...) and
// composite literal keys (authConfig{passwordResetExempt: ...}) — returning a
// diagnostic for every write whose file is not in allowlist, plus the observed write
// files.
func scanPasswordResetExemptFieldWrites(t *testing.T, allowlist map[string]struct{}) ([]Diagnostic, map[string]struct{}) {
	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		flag := func(rel string, pos token.Pos) {
			observed[rel] = struct{}{}
			if _, ok := allowlist[rel]; ok {
				return
			}
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(pos).Line,
				Message: fmt.Sprintf(
					"GRPC-PASSWORD-RESET-EXEMPT-WIRING-FUNNEL-01: authConfig.passwordResetExempt (the gRPC "+
						"password-reset-exempt state slot) is written from %s, which is not the sanctioned "+
						"writer. Only WithPasswordResetExempt in runtime/grpc/interceptor/auth.go may set "+
						"this field; writing it elsewhere widens the exempt set without the registrar single "+
						"source (#1382) — a latent auth bypass the WithPasswordResetExempt-reference scan "+
						"alone would miss. Route password-reset-exempt methods through the contract overlay "+
						"(endpoints.grpc.methods[].passwordResetExempt:true). If this IS a new sanctioned "+
						"writer, add it to passwordResetExemptFieldWriteAllowlist with rationale.",
					rel),
			})
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			// Form A: assignment LHS — c.passwordResetExempt = ...
			EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
				EachInChildren[ast.SelectorExpr](as, func(sel *ast.SelectorExpr) {
					if sel.Sel.Name != "passwordResetExempt" {
						return
					}
					// Restrict to the Lhs write targets so a RHS READ of
					// c.passwordResetExempt is not mis-flagged as a write.
					if !exprInList(as.Lhs, sel) {
						return
					}
					if s := p.TypesInfo.Selections[sel]; s != nil && isAuthConfigPasswordResetExemptField(s.Obj()) {
						flag(rel, sel.Pos())
					}
				})
			})
			// Form B: composite literal key — authConfig{passwordResetExempt: ...}
			EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
				EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
					if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "passwordResetExempt" &&
						isAuthConfigPasswordResetExemptField(p.TypesInfo.Uses[key]) {
						flag(rel, key.Pos())
					}
				})
			})
		}
		return d
	})
	return diags, observed
}

// withPasswordResetExemptCallerAllowlist is the set of production files permitted to
// reference interceptor.WithPasswordResetExempt. The sole sanctioned site is
// authChainOptions in chain.go, which installs
// WithPasswordResetExempt(reg.IsPasswordResetExemptMethod) for both the unary and
// stream chains; stream.go routes through that helper and holds no direct reference.
var withPasswordResetExemptCallerAllowlist = map[string]struct{}{
	"runtime/grpc/interceptor/chain.go": {},
}

// isWithPasswordResetExemptFunc reports whether id resolves (via go/types Uses) to
// the interceptor.WithPasswordResetExempt function object. Scanning all idents
// catches both the in-package bare-ident reference (chain.go) and any cross-package
// interceptor.WithPasswordResetExempt selector (the .Sel ident also resolves to the
// func). The func DECLARATION's name ident is in Defs, not Uses, so it is not
// matched.
func isWithPasswordResetExemptFunc(info *types.Info, id *ast.Ident) bool {
	fn, ok := info.Uses[id].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == grpcInterceptorPkgPath && fn.Name() == "WithPasswordResetExempt"
}

// scanWithPasswordResetExemptRefs scans production code for references to
// interceptor.WithPasswordResetExempt, returning a diagnostic for every reference
// whose file is not in allowlist, plus the set of files where a reference was
// observed. Parameterizing the allowlist lets the negative-control test reuse the
// identical scan with an empty allowlist.
func scanWithPasswordResetExemptRefs(t *testing.T, allowlist map[string]struct{}) ([]Diagnostic, map[string]struct{}) {
	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				if !isWithPasswordResetExemptFunc(p.TypesInfo, id) {
					return
				}
				observed[rel] = struct{}{}
				if _, allowed := allowlist[rel]; allowed {
					return
				}
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(id.Pos()).Line,
					Message: fmt.Sprintf(
						"GRPC-PASSWORD-RESET-EXEMPT-WIRING-FUNNEL-01: interceptor.WithPasswordResetExempt "+
							"is referenced from %s, which is not the sanctioned wiring site. The registrar is "+
							"the single runtime source of the gRPC password-reset-exempt set (#1382): the only "+
							"sanctioned installer is authChainOptions in "+
							"runtime/grpc/interceptor/chain.go, which wires "+
							"WithPasswordResetExempt(reg.IsPasswordResetExemptMethod). "+
							"WithPasswordResetExempt predicates compose (OR), so a composition root passing "+
							"one via Deps.AuthOptions would WIDEN the exempt set beyond the contract-derived "+
							"overlay — a latent auth bypass. Declare password-reset-exempt methods via "+
							"endpoints.grpc.methods[] (passwordResetExempt:true). If this IS a new sanctioned "+
							"wiring site, add it to withPasswordResetExemptCallerAllowlist with rationale.",
						rel,
					),
				})
			})
		}
		return d
	})
	return diags, observed
}

// TestArchtest_GRPCPasswordResetExemptWiringFunnel01 asserts that every production
// reference to interceptor.WithPasswordResetExempt sits in the caller allowlist, and
// that the sole allowlisted file actually hosts a live reference (anti-vacuity).
func TestArchtest_GRPCPasswordResetExemptWiringFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags, observed := scanWithPasswordResetExemptRefs(t, withPasswordResetExemptCallerAllowlist)

	// Anti-vacuity / no-stale reverse self-check: the sole allowlisted file must
	// host a live reference. A stale entry is a latent bypass slot.
	for f := range withPasswordResetExemptCallerAllowlist {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"GRPC-PASSWORD-RESET-EXEMPT-WIRING-FUNNEL-01: allowlist entry %q is STALE — no live "+
						"interceptor.WithPasswordResetExempt reference observed. The wiring site moved or the "+
						"scanner regressed; update the allowlist so a dead entry cannot become a silent bypass "+
						"slot.",
					f,
				),
			})
		}
	}

	// Dimension 2 (review F1): lock WRITES to the authConfig.passwordResetExempt
	// field, not just WithPasswordResetExempt references — a same-package AuthOption
	// could set the slot directly and bypass dimension 1.
	fieldDiags, fieldObserved := scanPasswordResetExemptFieldWrites(t, passwordResetExemptFieldWriteAllowlist)
	diags = append(diags, fieldDiags...)
	for f := range passwordResetExemptFieldWriteAllowlist {
		if _, seen := fieldObserved[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"GRPC-PASSWORD-RESET-EXEMPT-WIRING-FUNNEL-01: field-write allowlist entry %q is STALE "+
						"— no live authConfig.passwordResetExempt write observed. WithPasswordResetExempt "+
						"moved or the scanner regressed; update the allowlist so a dead entry cannot become a "+
						"silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "GRPC-PASSWORD-RESET-EXEMPT-WIRING-FUNNEL-01", diags)
}

// TestArchtest_GRPCPasswordResetExemptWiringFunnel01_NegativeControl is the
// synthetic red case: running the identical scan with an EMPTY allowlist must flag
// the live chain.go reference, proving the matcher fires on a real reference (not
// vacuously green) and that an out-of-allowlist reference produces a diagnostic.
func TestArchtest_GRPCPasswordResetExemptWiringFunnel01_NegativeControl(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags, observed := scanWithPasswordResetExemptRefs(t, map[string]struct{}{})

	require.NotEmpty(t, diags,
		"negative control: an empty allowlist must flag the live WithPasswordResetExempt reference")
	require.Contains(t, observed, "runtime/grpc/interceptor/chain.go",
		"negative control: chain.go must host the sanctioned WithPasswordResetExempt reference")
	found := false
	for _, d := range diags {
		if d.Rel == "runtime/grpc/interceptor/chain.go" {
			found = true
		}
	}
	assert.True(t, found,
		"negative control: the chain.go reference must be the flagged out-of-allowlist diagnostic")

	// Dimension 2: empty allowlist must flag the live authConfig.passwordResetExempt
	// field write in auth.go, proving the field-write scan is not vacuously green.
	fieldDiags, fieldObserved := scanPasswordResetExemptFieldWrites(t, map[string]struct{}{})
	require.NotEmpty(t, fieldDiags,
		"negative control: an empty allowlist must flag the live authConfig.passwordResetExempt field write")
	require.Contains(t, fieldObserved, "runtime/grpc/interceptor/auth.go",
		"negative control: auth.go must host the sanctioned authConfig.passwordResetExempt write (WithPasswordResetExempt)")
}
