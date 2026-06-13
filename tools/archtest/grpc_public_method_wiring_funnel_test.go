//go:build archtest

// Package archtest — grpc_public_method_wiring_funnel_test.go
//
// INVARIANT: GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01
//
// The runtime registrar is the SINGLE source of the gRPC public-method bypass set
// (#1675). The only sanctioned production installer of the auth interceptor's
// WithPublicMethod predicate is authOptionsWithPublicMethods in
// runtime/grpc/interceptor/chain.go, which wires WithPublicMethod(reg.IsPublicMethod)
// for both the unary and stream chains (stream.go routes through that helper, so it
// holds no direct reference). Public methods are declared in the contract via
// endpoints.grpc.methods[] (public:true), derived by cellgen into
// GRPCServiceSpec.PublicMethods, and aggregated by the registrar.
//
// This archtest forbids any OTHER production reference to interceptor.WithPublicMethod.
// A composition root passing its own WithPublicMethod predicate (e.g. via
// Deps.AuthOptions) would be a misleading-dead option — the chain appends
// reg.IsPublicMethod LAST, so the registrar deterministically overrides it
// (WithPublicMethod overwrites when non-nil) — and, if that append order ever
// regressed, a latent bypass of the contract-derived overlay. Forbidding it makes
// the registrar the provably-sole production source. It is the runtime-side sibling
// of the codegen-side locks (cellgen golden + contractgen overlay referential
// pre-pass + governance FMT-41) and mirrors GRPC-CHAIN-UNARY-INTERCEPTOR-CALLER-01.
//
// # AI-robust rating (per .claude/rules/gocell/ai-robust.md)
//
// Medium — a go/types caller-allowlist typed scan, same tier and mechanism as
// GRPC-CHAIN-UNARY-INTERCEPTOR-CALLER-01. Hard-downstream is not reachable:
// WithPublicMethod is an exported function; the single-source guarantee is the
// append-last wiring (chain.go) plus this allowlist, not type sealing. Upstream
// (contract → registrar) is locked by the codegen golden + contractgen pre-pass.
//
// # Blind spots (per AI-robust §"强制盲区自检")
//
//   - A caller that obtains WithPublicMethod through a variable/parameter typed as
//     AuthOption (not a direct reference to the func object) escapes the ident scan
//     — the same alias blind spot CALLER-01 documents. The value-ref case
//     (f := WithPublicMethod; opt := f(...)) IS caught: the RHS ident resolves to
//     the func object via go/types Uses.
//   - The scan is production-only (Production excludes _test.go); tests freely use
//     WithPublicMethod to exercise the predicate.
//
// Anti-vacuity: the sole allowlisted file must host a live reference (stale-entry
// reverse check below), and TestArchtest_GRPCPublicMethodWiringFunnel01_NegativeControl
// runs the identical scan with an EMPTY allowlist and asserts the live chain.go
// reference IS flagged — proving the matcher is not vacuously green.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withPublicMethodCallerAllowlist is the set of production files permitted to
// reference interceptor.WithPublicMethod. The sole sanctioned site is
// authOptionsWithPublicMethods in chain.go, which installs
// WithPublicMethod(reg.IsPublicMethod) for both the unary and stream chains;
// stream.go routes through that helper and holds no direct reference.
var withPublicMethodCallerAllowlist = map[string]struct{}{
	"runtime/grpc/interceptor/chain.go": {},
}

// isWithPublicMethodFunc reports whether id resolves (via go/types Uses) to the
// interceptor.WithPublicMethod function object. Scanning all idents catches both
// the in-package bare-ident reference (chain.go) and any cross-package
// interceptor.WithPublicMethod selector (the .Sel ident also resolves to the func).
// The func DECLARATION's name ident is in Defs, not Uses, so it is not matched.
func isWithPublicMethodFunc(info *types.Info, id *ast.Ident) bool {
	fn, ok := info.Uses[id].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == grpcInterceptorPkgPath && fn.Name() == "WithPublicMethod"
}

// scanWithPublicMethodRefs scans production code for references to
// interceptor.WithPublicMethod, returning a diagnostic for every reference whose
// file is not in allowlist, plus the set of files where a reference was observed.
// Parameterizing the allowlist lets the negative-control test reuse the identical
// scan with an empty allowlist.
func scanWithPublicMethodRefs(t *testing.T, allowlist map[string]struct{}) ([]Diagnostic, map[string]struct{}) {
	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				if !isWithPublicMethodFunc(p.TypesInfo, id) {
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
						"GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01: interceptor.WithPublicMethod is referenced from "+
							"%s, which is not the sanctioned wiring site. The registrar is the single runtime source "+
							"of the gRPC public-method bypass set (#1675): the only sanctioned installer is "+
							"authOptionsWithPublicMethods in runtime/grpc/interceptor/chain.go, which wires "+
							"WithPublicMethod(reg.IsPublicMethod). Passing a WithPublicMethod predicate from a "+
							"composition root (e.g. via Deps.AuthOptions) is a dead option (the registrar is appended "+
							"last and overrides it) and a latent bypass of the contract-derived overlay. Declare public "+
							"methods via endpoints.grpc.methods[] (public:true). If this IS a new sanctioned wiring "+
							"site, add it to withPublicMethodCallerAllowlist with rationale.",
						rel,
					),
				})
			})
		}
		return d
	})
	return diags, observed
}

// TestArchtest_GRPCPublicMethodWiringFunnel01 asserts that every production
// reference to interceptor.WithPublicMethod sits in the caller allowlist, and that
// the sole allowlisted file actually hosts a live reference (anti-vacuity).
func TestArchtest_GRPCPublicMethodWiringFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags, observed := scanWithPublicMethodRefs(t, withPublicMethodCallerAllowlist)

	// Anti-vacuity / no-stale reverse self-check: the sole allowlisted file must
	// host a live reference. A stale entry is a latent bypass slot.
	for f := range withPublicMethodCallerAllowlist {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01: allowlist entry %q is STALE — no live "+
						"interceptor.WithPublicMethod reference observed. The wiring site moved or the scanner "+
						"regressed; update the allowlist so a dead entry cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01", diags)
}

// TestArchtest_GRPCPublicMethodWiringFunnel01_NegativeControl is the synthetic red
// case: running the identical scan with an EMPTY allowlist must flag the live
// chain.go reference, proving the matcher fires on a real reference (not vacuously
// green) and that an out-of-allowlist reference produces a diagnostic.
func TestArchtest_GRPCPublicMethodWiringFunnel01_NegativeControl(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags, observed := scanWithPublicMethodRefs(t, map[string]struct{}{})

	require.NotEmpty(t, diags,
		"negative control: an empty allowlist must flag the live WithPublicMethod reference")
	require.Contains(t, observed, "runtime/grpc/interceptor/chain.go",
		"negative control: chain.go must host the sanctioned WithPublicMethod reference")
	found := false
	for _, d := range diags {
		if d.Rel == "runtime/grpc/interceptor/chain.go" {
			found = true
		}
	}
	assert.True(t, found,
		"negative control: the chain.go reference must be the flagged out-of-allowlist diagnostic")
}
