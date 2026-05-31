// Package archtest — grpc_interceptor_chain_invariants_test.go
//
//   - INVARIANT: GRPC-INTERCEPTOR-CHAIN-ORDER-01
//   - INVARIANT: GRPC-CHAIN-UNARY-INTERCEPTOR-CALLER-01
//
// Two related invariants guard the single unary interceptor chain that
// runtime/grpc/interceptor.NewUnaryChain composes:
//
//   - ORDER-01 pins the argument ORDER of the grpc.ChainUnaryInterceptor call
//     inside NewUnaryChain (RequestID outermost … Recovery innermost), with each
//     argument's callee resolved to the real runtime/grpc/interceptor
//     constructor via go/types (not a bare name match).
//   - CALLER-01 pins WHO may call grpc.ChainUnaryInterceptor at all: the sole
//     sanctioned composition site is NewUnaryChain. Any other caller (bootstrap,
//     cmd, a cell) that composes its own chain would bypass ORDER-01 entirely.
//
// # GRPC-INTERCEPTOR-CHAIN-ORDER-01
//
// runtime/grpc/interceptor/chain.go composes the unary interceptor chain via a
// single grpc.ChainUnaryInterceptor(...) call inside NewUnaryChain. That call's
// arguments MUST be, in order:
//
//	UnaryRequestID, UnaryTracing, UnaryMetrics, UnaryAuth, UnaryRecovery
//
// i.e. RequestID outermost and Recovery innermost. The order is load-bearing:
//   - RequestID outermost so every other interceptor (and any errcode the
//     handler emits) carries a stable request/correlation id.
//   - Recovery innermost so a handler panic is collapsed into codes.Internal
//     *before* the outer Metrics and Tracing interceptors observe the result;
//     otherwise a panic would be recorded as a raw failure rather than a clean
//     Internal status (see runtime/grpc/interceptor package doc + chain_test.go
//     behavioral guard).
//
// ## AI-robust rating (not a funnel; single-axis ordering + callee identity)
//
//   - Callee identity: MEDIUM, type-aware. Each argument's callee is resolved
//     through go/types (ResolvePackageRef on pass.TypesInfo) and asserted to be
//     the exact runtime/grpc/interceptor.Unary{RequestID,Tracing,Metrics,Auth,
//     Recovery} function — an import alias, a dot-import, or a same-named
//     constructor from a different package all resolve to a different (or no)
//     types object and fail. This closes the pre-F3 "bare *ast.Ident.Name" gap
//     where a same-named decoy from another package would have mimicked the
//     order. Hard is not reachable on this axis: Go cannot make "wrong
//     constructor identity in an argument list" uncompilable.
//   - Ordering: MEDIUM, PERMANENT CEILING. Interceptor order is statement /
//     argument order in a function body; the Go type system cannot make "wrong
//     order = uncompilable" (the HTTP middleware order in runtime/http/router is
//     likewise archtest-free). NewUnaryChain is the single sanctioned
//     composition point (enforced by CALLER-01 below + the blind-spot check),
//     and chain_test.go behaviorally verifies the recovery-innermost
//     consequence; this archtest is the static backstop against future
//     reordering.
//
// ## Tool blind spots (charter §"强制盲区自检")
//
//   - A second grpc.ChainUnaryInterceptor call elsewhere in the interceptor
//     package with a different order would bypass the order assertion's
//     single-site read. Reverse self-check:
//     TestArchtest_GRPCInterceptorChainOrder_BlindSpot_SingleCompositionSite
//     asserts the interceptor package contains EXACTLY ONE such call (resolved
//     via go/types), so the order scan covers the sole site. Repo-wide, CALLER-01
//     extends the single-site guarantee beyond the package.
//   - An argument that is not a direct constructor CallExpr (a variable, a
//     spread xs..., a wrapped call) would make the per-argument callee
//     resolution return a "<...>" marker so the order assertion rejects it
//     rather than vacuously passing. Reverse self-check:
//     TestArchtest_GRPCInterceptorChainOrder_BlindSpot_ArgsResolveToConstructors.
//
// # GRPC-CHAIN-UNARY-INTERCEPTOR-CALLER-01
//
// grpc.ChainUnaryInterceptor (google.golang.org/grpc) is the third-party
// primitive that assembles a unary interceptor slice into a single
// grpc.ServerOption. GoCell composes its chain in exactly one place —
// runtime/grpc/interceptor/chain.go::NewUnaryChain — so that ORDER-01 has a
// single authoritative site to guard. Without this caller-allowlist, a future
// bootstrap / cmd / cell could call grpc.ChainUnaryInterceptor directly with an
// arbitrary order, installing an unguarded chain that ORDER-01's single-site
// read never sees.
//
// This archtest pins the production callsite identity of grpc.ChainUnaryInterceptor
// to:
//
//	{ runtime/grpc/interceptor/chain.go::NewUnaryChain }
//
// ## AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: MEDIUM by archtest caller-allowlist. The callee is resolved
//     via go/types (ResolvePackageRef), so import aliases (e.g.
//     grpc2 "google.golang.org/grpc") and function-value references resolve to
//     the same symbol — there is no "looks like but isn't" gap. Any callsite
//     outside the allowlist fails in CI. The form is unique (qualified ref to
//     the grpc package's ChainUnaryInterceptor). Hard-downstream is not reachable
//     by sealing because the symbol is third-party; the allowlist + go/types
//     resolution is the strongest static form available.
//   - Upstream: HARD is UNREACHABLE — a GO-LANGUAGE CEILING, not a deferred
//     TODO. grpc.ChainUnaryInterceptor is an EXPORTED function in a third-party
//     module; Go visibility cannot express "only runtime/grpc/interceptor may
//     call this exported func". This is the same permanent ceiling documented
//     for SPAN-SETATTR-HOLDER-SEAL (#851) and the cross-package caller-allowlist
//     funnels CTXKEYS-PRINCIPAL-WRITE-CALLER-01 / OUTBOX-RECONSTRUCTION-CALLER-01
//     (#1282). Tracked as a won't-do hard-upstream item under gh #1394; the
//     downstream archtest is the enforcement.
//
// ## Detection is REFERENCE-based, not call-based
//
// The scanner matches every SelectorExpr that go/types resolves to
// grpc.ChainUnaryInterceptor — whether it is the callee of a call OR passed as a
// function value (e.g. f := grpc.ChainUnaryInterceptor; opt := f(...)). A
// call-only scanner would miss the function-value indirection.
//
// ## Tool blind spots (charter §"强制盲区自检")
//
//   - Dot-import bare-identifier form (import . "google.golang.org/grpc";
//     ChainUnaryInterceptor(...)) references the symbol as a bare *ast.Ident
//     nested in a call; the SelectorExpr walk would miss it, but it would
//     resolve through TypesInfo if surfaced. Dot-importing grpc is absent from
//     the corpus and conspicuous; documented, not enforced.
//   - A call added in a //go:build-gated PRODUCTION file under a non-default tag
//     would be missed by the default-tags scan. The composition site today is
//     default-build; documented.
//   - The anti-vacuity guard (the sole allowlisted callsite must be observed
//     ≥1×) is the reverse self-check: it proves the scanner resolves the real
//     reference and forbids stale-allowlist rot (a dead entry is a latent bypass
//     slot).
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// grpcInterceptorPkgPath is the import path of the package that owns the unary
// interceptor constructors and the NewUnaryChain composition site.
const grpcInterceptorPkgPath = "github.com/ghbvf/gocell/runtime/grpc/interceptor"

// grpcPkgPath is the third-party gRPC package owning ChainUnaryInterceptor.
const grpcPkgPath = "google.golang.org/grpc"

// grpcChainExpectedOrder is the required argument order of the
// grpc.ChainUnaryInterceptor call in NewUnaryChain, by constructor name. Each
// name is resolved to a runtime/grpc/interceptor function via go/types before
// the order is compared, so a same-named decoy from another package will not
// match.
var grpcChainExpectedOrder = []string{
	"UnaryRequestID",
	"UnaryTracing",
	"UnaryMetrics",
	"UnaryAuth",
	"UnaryRecovery",
}

// isChainUnaryInterceptorSelector resolves sel via go/types and reports whether
// it references grpc.ChainUnaryInterceptor (alias/value-ref proof).
func isChainUnaryInterceptorSelector(info *types.Info, sel *ast.SelectorExpr) bool {
	pkgPath, name, ok := ResolvePackageRef(info, sel)
	return ok && pkgPath == grpcPkgPath && name == "ChainUnaryInterceptor"
}

// TestArchtest_GRPCInterceptorChainOrder asserts the interceptor argument order
// of the single grpc.ChainUnaryInterceptor call in chain.go, resolving each
// argument's callee to the real runtime/grpc/interceptor constructor via
// go/types (F3: typed callee resolution, not bare name match).
func TestArchtest_GRPCInterceptorChainOrder(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	found := false
	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg.Path() != grpcInterceptorPkgPath {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !isChainUnaryInterceptorSelector(p.TypesInfo, sel) {
					return
				}
				found = true
				got := grpcResolveArgConstructors(p.TypesInfo, call)
				if !equalStrings(grpcChainExpectedOrder, got) {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(call.Pos()).Line,
						Message: fmt.Sprintf(
							"GRPC-INTERCEPTOR-CHAIN-ORDER-01: interceptor order must be "+
								"RequestID→Tracing→Metrics→Auth→Recovery (RequestID outermost, Recovery "+
								"innermost), each resolved to a runtime/grpc/interceptor constructor; got %v. "+
								"See runtime/grpc/interceptor package doc.",
							got),
					})
				}
			})
		}
		return d
	})

	require.True(t, found,
		"GRPC-INTERCEPTOR-CHAIN-ORDER-01: no go/types-resolved grpc.ChainUnaryInterceptor "+
			"call found in %s — the order scan would vacuously pass.", grpcInterceptorPkgPath)
	Report(t, "GRPC-INTERCEPTOR-CHAIN-ORDER-01", diags)
}

// grpcResolveArgConstructors resolves each argument of the
// grpc.ChainUnaryInterceptor call to the simple name of the
// runtime/grpc/interceptor constructor it invokes. Any argument that is not a
// direct call to a runtime/grpc/interceptor package function yields a "<...>"
// marker so the order comparison rejects it (rather than vacuously passing on a
// missing/odd argument).
func grpcResolveArgConstructors(info *types.Info, call *ast.CallExpr) []string {
	if call.Ellipsis != token.NoPos {
		// A spread (xs...) defeats positional order analysis entirely.
		return []string{"<spread-args>"}
	}
	out := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		out = append(out, grpcResolveArgConstructor(info, arg))
	}
	return out
}

// grpcResolveArgConstructor resolves a single argument expression to the simple
// name of the runtime/grpc/interceptor constructor it invokes, or a "<...>"
// diagnostic marker for any non-conforming form.
func grpcResolveArgConstructor(info *types.Info, arg ast.Expr) string {
	c, ok := arg.(*ast.CallExpr)
	if !ok {
		return fmt.Sprintf("<not-a-call:%T>", arg)
	}
	switch fn := c.Fun.(type) {
	case *ast.SelectorExpr:
		pkgPath, name, ok := ResolvePackageRef(info, fn)
		if !ok || pkgPath != grpcInterceptorPkgPath {
			return fmt.Sprintf("<unresolved:%s.%s>", selPkgText(fn), fn.Sel.Name)
		}
		return name
	case *ast.Ident:
		// Bare identifier callee (same-package or dot-import): resolve via Uses.
		if obj := info.Uses[fn]; obj != nil {
			if f, ok := obj.(*types.Func); ok && f.Pkg() != nil &&
				f.Pkg().Path() == grpcInterceptorPkgPath {
				return f.Name()
			}
		}
		return fmt.Sprintf("<unresolved-ident:%s>", fn.Name)
	default:
		return fmt.Sprintf("<unresolved-callee:%T>", c.Fun)
	}
}

// selPkgText returns the textual qualifier of a selector for diagnostics only.
func selPkgText(sel *ast.SelectorExpr) string {
	if id, ok := sel.X.(*ast.Ident); ok {
		return id.Name
	}
	return "?"
}

// equalStrings reports slice equality (small helper to keep the rule body
// allocation-light; testify is reserved for the blind-spot assertions).
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestArchtest_GRPCInterceptorChainOrder_BlindSpot_SingleCompositionSite is the
// reverse self-check that the interceptor package (production files) contains
// EXACTLY ONE grpc.ChainUnaryInterceptor call, so the order scan above covers
// the sole composition site within the package. A second composition elsewhere
// in the package could install a differently-ordered chain that the order scan's
// single read misses. Resolution is via go/types (RunTypedProduction), not raw
// file parsing, to stay within the archtest framework (SCANNER-FRAMEWORK-USAGE).
func TestArchtest_GRPCInterceptorChainOrder_BlindSpot_SingleCompositionSite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var locations []string
	RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg.Path() != grpcInterceptorPkgPath {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !isChainUnaryInterceptorSelector(p.TypesInfo, sel) {
					return
				}
				locations = append(locations,
					fmt.Sprintf("%s:%d", rel, p.Fset.Position(call.Pos()).Line))
			})
		}
		return nil
	})

	assert.Len(t, locations, 1,
		"GRPC-INTERCEPTOR-CHAIN-ORDER-01 blind-spot: grpc.ChainUnaryInterceptor must be called "+
			"exactly once in the interceptor package (sole composition site); found at %v", locations)
}

// TestArchtest_GRPCInterceptorChainOrder_BlindSpot_ArgsResolveToConstructors is
// the reverse self-check that every argument to the chain.go
// grpc.ChainUnaryInterceptor call resolves (via go/types) to a
// runtime/grpc/interceptor constructor. If an argument were a variable, a spread
// (xs...), or a wrapped call, grpcResolveArgConstructor would emit a "<...>"
// marker and this check would fail — proving the order assertion above cannot
// vacuously pass on a malformed argument list.
func TestArchtest_GRPCInterceptorChainOrder_BlindSpot_ArgsResolveToConstructors(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var violations []string
	found := false
	RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg.Path() != grpcInterceptorPkgPath {
			return nil
		}
		for _, file := range p.Files {
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !isChainUnaryInterceptorSelector(p.TypesInfo, sel) {
					return
				}
				found = true
				for i, c := range grpcResolveArgConstructors(p.TypesInfo, call) {
					if strings.HasPrefix(c, "<") {
						violations = append(violations,
							fmt.Sprintf("arg %d did not resolve to an interceptor constructor: %s", i, c))
					}
				}
			})
		}
		return nil
	})

	require.True(t, found, "GRPC-INTERCEPTOR-CHAIN-ORDER-01 blind-spot: no chain composition site found")
	assert.Empty(t, violations,
		"GRPC-INTERCEPTOR-CHAIN-ORDER-01 blind-spot: every grpc.ChainUnaryInterceptor argument must "+
			"resolve to a runtime/grpc/interceptor constructor; otherwise order enforcement is bypassable.")
}

// chainUnaryInterceptorCallerAllowlist is the set of production files allowed to
// reference grpc.ChainUnaryInterceptor. The single sanctioned composition site
// is NewUnaryChain in chain.go.
var chainUnaryInterceptorCallerAllowlist = map[string]struct{}{
	"runtime/grpc/interceptor/chain.go": {}, // NewUnaryChain — sole composition site
}

// TestArchtest_GRPCChainUnaryInterceptorCaller01 asserts that every production
// reference to grpc.ChainUnaryInterceptor sits in the caller allowlist, and that
// the sole allowlisted file is actually observed (anti-vacuity reverse check).
func TestArchtest_GRPCChainUnaryInterceptorCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}

	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if !isChainUnaryInterceptorSelector(p.TypesInfo, sel) {
					return
				}
				observed[rel] = struct{}{}
				if _, allowed := chainUnaryInterceptorCallerAllowlist[rel]; !allowed {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(sel.Pos()).Line,
						Message: fmt.Sprintf(
							"GRPC-CHAIN-UNARY-INTERCEPTOR-CALLER-01: grpc.ChainUnaryInterceptor is referenced "+
								"from %s, which is not the sanctioned chain-composition site. Composing a unary "+
								"interceptor chain anywhere but runtime/grpc/interceptor.NewUnaryChain bypasses the "+
								"chain-order invariant (GRPC-INTERCEPTOR-CHAIN-ORDER-01). Route all gRPC unary "+
								"interceptor assembly through NewUnaryChain. If this IS a new sanctioned composition "+
								"site, add it to chainUnaryInterceptorCallerAllowlist with rationale and extend "+
								"ORDER-01 to cover it.",
							rel),
					})
				}
			})
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check: the sole allowlisted file must
	// host a live reference. A stale entry is a latent bypass slot.
	for f := range chainUnaryInterceptorCallerAllowlist {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"GRPC-CHAIN-UNARY-INTERCEPTOR-CALLER-01: allowlist entry %q is STALE — no live "+
						"grpc.ChainUnaryInterceptor reference observed. Either the scanner regressed or the "+
						"composition site moved; drop or update the dead allowlist entry so it cannot become a "+
						"silent bypass slot.",
					f),
			})
		}
	}

	Report(t, "GRPC-CHAIN-UNARY-INTERCEPTOR-CALLER-01", diags)
}
