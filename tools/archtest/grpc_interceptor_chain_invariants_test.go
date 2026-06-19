//go:build archtest

// Package archtest — grpc_interceptor_chain_invariants_test.go
//
//   - INVARIANT: GRPC-INTERCEPTOR-CHAIN-ORDER-01
//   - INVARIANT: GRPC-CHAIN-UNARY-INTERCEPTOR-CALLER-01
//   - INVARIANT: GRPC-STREAM-CHAIN-ORDER-01
//   - INVARIANT: GRPC-CHAIN-STREAM-INTERCEPTOR-CALLER-01
//   - INVARIANT: GRPC-STREAM-DRAIN-01
//   - INVARIANT: GRPC-WIRING-REGISTRAR-MINT-FUNNEL-01
//   - INVARIANT: GRPC-WIRING-BUNDLE-CALLER-01
//
// The STREAM-* invariants (PR-10 #1153) are the streaming counterparts of the
// unary chain guards, with the same AI-robust ratings and Go-ceiling caveats:
// GRPC-STREAM-CHAIN-ORDER-01 pins the 8-arg order of the single
// grpc.ChainStreamInterceptor call in newStreamChain (RequestID outermost, Drain
// just inside Auth, Recovery innermost); GRPC-CHAIN-STREAM-INTERCEPTOR-CALLER-01
// pins WHO may call grpc.ChainStreamInterceptor (sole site = newStreamChain in
// stream.go) — Hard-upstream is the same Go-language ceiling as the unary
// CALLER-01 (third-party exported func, won't-do gh #1394). The single-wiring-object
// upgrade that also forces both chains to be wired is DONE (gh #1752): the chain
// builders are package-private and NewServerInterceptors is the sole funnel that
// mints the registrar/drain (GRPC-WIRING-REGISTRAR-MINT-FUNNEL-01 below).
// GRPC-STREAM-DRAIN-01 is the two-sided framework-drain guard: (A) StreamDrain is
// a pinned arg of newStreamChain (every in-flight stream's ctx is bound to the
// drain signal), and (B) runtimegrpc.DrainSignal.Trigger is caller-allowlisted to
// adapters/grpc/server.go (only — and exactly — the adapter's gracefulStop fires
// it). Together: drain is wired on both producer (adapter Trigger) and consumer
// (chain StreamDrain) ends. Upstream Hard is unreachable (Trigger is an exported
// method; Go cannot seal its callers) — same #1394/#851/#1282 family; the
// single-wiring-object same-instance upgrade landed in gh #1752.
//
// Two related invariants guard the single unary interceptor chain that
// runtime/grpc/interceptor.newUnaryChain composes:
//
//   - ORDER-01 pins the argument ORDER of the grpc.ChainUnaryInterceptor call
//     inside newUnaryChain (RequestID outermost … Recovery innermost), with each
//     argument's callee resolved to the real runtime/grpc/interceptor
//     constructor via go/types (not a bare name match).
//   - CALLER-01 pins WHO may call grpc.ChainUnaryInterceptor at all: the sole
//     sanctioned composition site is newUnaryChain. Any other caller (bootstrap,
//     cmd, a cell) that composes its own chain would bypass ORDER-01 entirely.
//
// # GRPC-INTERCEPTOR-CHAIN-ORDER-01
//
// runtime/grpc/interceptor/chain.go composes the unary interceptor chain via a
// single grpc.ChainUnaryInterceptor(...) call inside newUnaryChain. That call's
// arguments MUST be, in order:
//
//	UnaryRequestID, UnaryCellAttribution, UnaryTracing, UnaryAccessLog,
//	UnaryMetrics, UnaryAuth, UnaryRecovery
//
// i.e. RequestID outermost and Recovery innermost — mirroring the HTTP
// listener-root order (CellAttribution → Tracing → AccessLog → Metrics). The
// order is load-bearing:
//   - RequestID outermost so every other interceptor (and any errcode the
//     handler emits) carries a stable request/correlation id.
//   - CellAttribution before every interceptor that reads the cell label
//     (AccessLog, Metrics) so the owning cell is in ctx when they observe it.
//   - AccessLog after Tracing (so trace_id, set on a propagated trace, is in
//     ctx) and OUTER to Auth (so auth rejections are still logged).
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
//     likewise archtest-free). newUnaryChain is the single sanctioned
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
// runtime/grpc/interceptor/chain.go::newUnaryChain — so that ORDER-01 has a
// single authoritative site to guard. Without this caller-allowlist, a future
// bootstrap / cmd / cell could call grpc.ChainUnaryInterceptor directly with an
// arbitrary order, installing an unguarded chain that ORDER-01's single-site
// read never sees.
//
// This archtest pins the production callsite identity of grpc.ChainUnaryInterceptor
// to:
//
//	{ runtime/grpc/interceptor/chain.go::newUnaryChain }
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
// interceptor constructors and the newUnaryChain composition site. Anchored to
// [PlatformModulePath] so a module rename updates exactly one place.
const grpcInterceptorPkgPath = PlatformFrameworkModulePath + "/runtime/grpc/interceptor"

// grpcPkgPath is the third-party gRPC package owning ChainUnaryInterceptor.
const grpcPkgPath = "google.golang.org/grpc"

// grpcChainExpectedOrder is the required argument order of the
// grpc.ChainUnaryInterceptor call in newUnaryChain, by constructor name. Each
// name is resolved to a runtime/grpc/interceptor function via go/types before
// the order is compared, so a same-named decoy from another package will not
// match.
//
// PR-12 (#1155) extends the unary chain with RateLimit + CircuitBreaker (between
// Metrics and Auth, protection-chain parity with HTTP) and ErrcodeMap just outside
// Recovery (so errcode→grpc/codes mapping fires before Recovery's panic collapse
// is observed by the outer interceptors).
var grpcChainExpectedOrder = []string{
	"UnaryRequestID",
	"UnaryCellAttribution",
	"UnaryTracing",
	"UnaryAccessLog",
	"UnaryMetrics",
	"UnaryRateLimit",
	"UnaryCircuitBreaker",
	"UnaryAuth",
	"UnaryErrcodeMap",
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
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
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
								"RequestID→CellAttribution→Tracing→AccessLog→Metrics→RateLimit→CircuitBreaker→Auth→ErrcodeMap→Recovery "+
								"(RequestID outermost, Recovery innermost, RateLimit+CircuitBreaker between Metrics and Auth, "+
								"ErrcodeMap just outside Recovery), each resolved to a "+
								"runtime/grpc/interceptor constructor; got %v. "+
								"See runtime/grpc/interceptor package doc.",
							got,
						),
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
// single read misses. Resolution is via go/types (Run(t, Production(...))), not raw
// file parsing, to stay within the archtest framework (SCANNER-FRAMEWORK-USAGE).
func TestArchtest_GRPCInterceptorChainOrder_BlindSpot_SingleCompositionSite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var locations []string
	Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
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
	Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
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
// is newUnaryChain in chain.go.
var chainUnaryInterceptorCallerAllowlist = map[string]struct{}{
	"runtime/grpc/interceptor/chain.go": {}, // newUnaryChain — sole composition site
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

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
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
								"interceptor chain anywhere but runtime/grpc/interceptor.newUnaryChain bypasses the "+
								"chain-order invariant (GRPC-INTERCEPTOR-CHAIN-ORDER-01). Obtain the wiring bundle "+
								"from interceptor.NewServerInterceptors(deps) (the public funnel; newUnaryChain is "+
								"package-private). If this IS a new sanctioned composition site, add it to "+
								"chainUnaryInterceptorCallerAllowlist with rationale and extend ORDER-01 to cover it.",
							rel,
						),
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
					f,
				),
			})
		}
	}

	Report(t, "GRPC-CHAIN-UNARY-INTERCEPTOR-CALLER-01", diags)
}

// ─── Streaming chain (PR-10 #1153) ──────────────────────────────────────────────

// grpcRuntimePkgPath is the import path of the runtime/grpc package that owns the
// shared ServiceRegistrar and DrainSignal.
const grpcRuntimePkgPath = PlatformFrameworkModulePath + "/runtime/grpc"

// grpcStreamChainExpectedOrder is the required argument order of the
// grpc.ChainStreamInterceptor call in newStreamChain. The streaming chain mirrors
// the unary order plus a stream-only StreamDrain just inside StreamAuth (so the
// handler's context is drain-bound while the outer observability interceptors
// still see the final status). Each name is resolved to a runtime/grpc/interceptor
// function via go/types before the order is compared.
//
// PR-12 (#1155) extends the stream chain with StreamRateLimit + StreamCircuitBreaker
// (between Metrics and Auth) and StreamErrcodeMap just outside StreamRecovery —
// parallel to the unary extensions.
var grpcStreamChainExpectedOrder = []string{
	"StreamRequestID",
	"StreamCellAttribution",
	"StreamTracing",
	"StreamAccessLog",
	"StreamMetrics",
	"StreamRateLimit",
	"StreamCircuitBreaker",
	"StreamAuth",
	"StreamDrain",
	"StreamErrcodeMap",
	"StreamRecovery",
}

// isChainStreamInterceptorSelector resolves sel via go/types and reports whether
// it references grpc.ChainStreamInterceptor (alias/value-ref proof).
func isChainStreamInterceptorSelector(info *types.Info, sel *ast.SelectorExpr) bool {
	pkgPath, name, ok := ResolvePackageRef(info, sel)
	return ok && pkgPath == grpcPkgPath && name == "ChainStreamInterceptor"
}

// TestArchtest_GRPCStreamChainOrder asserts the interceptor argument order of the
// single grpc.ChainStreamInterceptor call in stream.go, resolving each argument's
// callee to the real runtime/grpc/interceptor constructor via go/types — the
// streaming sibling of GRPC-INTERCEPTOR-CHAIN-ORDER-01.
func TestArchtest_GRPCStreamChainOrder(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	found := false
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg.Path() != grpcInterceptorPkgPath {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !isChainStreamInterceptorSelector(p.TypesInfo, sel) {
					return
				}
				found = true
				got := grpcResolveArgConstructors(p.TypesInfo, call)
				if !equalStrings(grpcStreamChainExpectedOrder, got) {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(call.Pos()).Line,
						Message: fmt.Sprintf(
							"GRPC-STREAM-CHAIN-ORDER-01: stream interceptor order must be "+
								"RequestID→CellAttribution→Tracing→AccessLog→Metrics→RateLimit→CircuitBreaker→Auth→Drain→ErrcodeMap→Recovery "+
								"(RequestID outermost, Recovery innermost, RateLimit+CircuitBreaker between Metrics and Auth, "+
								"Drain just inside Auth, ErrcodeMap just outside Recovery), each "+
								"resolved to a runtime/grpc/interceptor constructor; got %v. "+
								"See runtime/grpc/interceptor package doc.",
							got,
						),
					})
				}
			})
		}
		return d
	})

	require.True(t, found,
		"GRPC-STREAM-CHAIN-ORDER-01: no go/types-resolved grpc.ChainStreamInterceptor "+
			"call found in %s — the order scan would vacuously pass.", grpcInterceptorPkgPath)
	Report(t, "GRPC-STREAM-CHAIN-ORDER-01", diags)
}

// TestArchtest_GRPCStreamChainOrder_BlindSpot_SingleCompositionSite is the reverse
// self-check that the interceptor package contains EXACTLY ONE
// grpc.ChainStreamInterceptor call, so the order scan covers the sole stream
// composition site within the package.
func TestArchtest_GRPCStreamChainOrder_BlindSpot_SingleCompositionSite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var locations []string
	Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg.Path() != grpcInterceptorPkgPath {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !isChainStreamInterceptorSelector(p.TypesInfo, sel) {
					return
				}
				locations = append(locations,
					fmt.Sprintf("%s:%d", rel, p.Fset.Position(call.Pos()).Line))
			})
		}
		return nil
	})

	assert.Len(t, locations, 1,
		"GRPC-STREAM-CHAIN-ORDER-01 blind-spot: grpc.ChainStreamInterceptor must be called "+
			"exactly once in the interceptor package (sole stream composition site); found at %v", locations)
}

// TestArchtest_GRPCStreamChainOrder_BlindSpot_ArgsResolveToConstructors is the
// reverse self-check that every argument to the stream.go
// grpc.ChainStreamInterceptor call resolves (via go/types) to a
// runtime/grpc/interceptor constructor, proving the order assertion cannot
// vacuously pass on a malformed argument list.
func TestArchtest_GRPCStreamChainOrder_BlindSpot_ArgsResolveToConstructors(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var violations []string
	found := false
	Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg.Path() != grpcInterceptorPkgPath {
			return nil
		}
		for _, file := range p.Files {
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !isChainStreamInterceptorSelector(p.TypesInfo, sel) {
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

	require.True(t, found, "GRPC-STREAM-CHAIN-ORDER-01 blind-spot: no stream chain composition site found")
	assert.Empty(t, violations,
		"GRPC-STREAM-CHAIN-ORDER-01 blind-spot: every grpc.ChainStreamInterceptor argument must "+
			"resolve to a runtime/grpc/interceptor constructor; otherwise order enforcement is bypassable.")
}

// chainStreamInterceptorCallerAllowlist is the set of production files allowed to
// reference grpc.ChainStreamInterceptor. The single sanctioned composition site
// is newStreamChain in stream.go.
var chainStreamInterceptorCallerAllowlist = map[string]struct{}{
	"runtime/grpc/interceptor/stream.go": {}, // newStreamChain — sole stream composition site
}

// TestArchtest_GRPCChainStreamInterceptorCaller01 asserts that every production
// reference to grpc.ChainStreamInterceptor sits in the caller allowlist, and that
// the sole allowlisted file is actually observed (anti-vacuity reverse check).
func TestArchtest_GRPCChainStreamInterceptorCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if !isChainStreamInterceptorSelector(p.TypesInfo, sel) {
					return
				}
				observed[rel] = struct{}{}
				if _, allowed := chainStreamInterceptorCallerAllowlist[rel]; !allowed {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(sel.Pos()).Line,
						Message: fmt.Sprintf(
							"GRPC-CHAIN-STREAM-INTERCEPTOR-CALLER-01: grpc.ChainStreamInterceptor is referenced "+
								"from %s, which is not the sanctioned stream-composition site. Composing a stream "+
								"interceptor chain anywhere but runtime/grpc/interceptor.newStreamChain bypasses the "+
								"chain-order invariant (GRPC-STREAM-CHAIN-ORDER-01). Obtain the wiring bundle from "+
								"interceptor.NewServerInterceptors(deps) (the public funnel; newStreamChain is "+
								"package-private). If this IS a new sanctioned composition site, add it to "+
								"chainStreamInterceptorCallerAllowlist with rationale and extend STREAM-CHAIN-ORDER-01 to cover it.",
							rel,
						),
					})
				}
			})
		}
		return d
	})

	for f := range chainStreamInterceptorCallerAllowlist {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"GRPC-CHAIN-STREAM-INTERCEPTOR-CALLER-01: allowlist entry %q is STALE — no live "+
						"grpc.ChainStreamInterceptor reference observed. Either the scanner regressed or the "+
						"composition site moved; drop or update the dead allowlist entry so it cannot become a "+
						"silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "GRPC-CHAIN-STREAM-INTERCEPTOR-CALLER-01", diags)
}

// isDrainSignalTrigger resolves sel's selector ident via go/types and reports
// whether it references the (*runtimegrpc.DrainSignal).Trigger method
// (receiver-bound, alias-proof).
func isDrainSignalTrigger(info *types.Info, sel *ast.SelectorExpr) bool {
	obj := info.Uses[sel.Sel]
	f, ok := obj.(*types.Func)
	if !ok || f.Name() != "Trigger" || f.Pkg() == nil || f.Pkg().Path() != grpcRuntimePkgPath {
		return false
	}
	sig, ok := f.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	rt := sig.Recv().Type()
	if ptr, ok := rt.(*types.Pointer); ok {
		rt = ptr.Elem()
	}
	named, ok := rt.(*types.Named)
	return ok && named.Obj().Name() == "DrainSignal"
}

// drainSignalTriggerCallerAllowlist is the set of production files allowed to call
// runtimegrpc.DrainSignal.Trigger. The sole sanctioned firing site is the
// adapter's gracefulStop, which triggers the drain before GracefulStop.
var drainSignalTriggerCallerAllowlist = map[string]struct{}{
	"adapters/grpc/server.go": {}, // gracefulStop — sole drain trigger
}

// TestArchtest_GRPCStreamDrain01 is the two-sided framework-drain guard:
//
//	(A) StreamDrain is a pinned argument of the grpc.ChainStreamInterceptor call
//	    in newStreamChain (consumer side — every in-flight stream's context is
//	    bound to the drain signal).
//	(B) runtimegrpc.DrainSignal.Trigger is called only — and is actually called —
//	    from adapters/grpc/server.go (producer side — the adapter's gracefulStop
//	    must fire the drain, and nothing else may).
func TestArchtest_GRPCStreamDrain01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observedTrigger := map[string]struct{}{}
	streamDrainInChain := false

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		// Part A: StreamDrain must be an argument of the stream chain composition.
		if p.Pkg.Path() == grpcInterceptorPkgPath {
			for _, file := range p.Files {
				EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || !isChainStreamInterceptorSelector(p.TypesInfo, sel) {
						return
					}
					for _, name := range grpcResolveArgConstructors(p.TypesInfo, call) {
						if name == "StreamDrain" {
							streamDrainInChain = true
						}
					}
				})
			}
		}
		// Part B: DrainSignal.Trigger caller allowlist.
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if !isDrainSignalTrigger(p.TypesInfo, sel) {
					return
				}
				observedTrigger[rel] = struct{}{}
				if _, allowed := drainSignalTriggerCallerAllowlist[rel]; !allowed {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(sel.Pos()).Line,
						Message: fmt.Sprintf(
							"GRPC-STREAM-DRAIN-01: runtimegrpc.DrainSignal.Trigger is called from %s, which is "+
								"not the sanctioned drain-firing site. Only the gRPC adapter's gracefulStop may "+
								"trigger the drain (it cancels in-flight streams before GracefulStop). Firing it "+
								"elsewhere would cut live streams. If this IS a new sanctioned site, add it to "+
								"drainSignalTriggerCallerAllowlist with rationale.",
							rel,
						),
					})
				}
			})
		}
		return d
	})

	// Part A anti-vacuity: StreamDrain must actually be wired into the chain.
	if !streamDrainInChain {
		diags = append(diags, Diagnostic{
			Message: "GRPC-STREAM-DRAIN-01 (A): StreamDrain is not a pinned argument of the " +
				"grpc.ChainStreamInterceptor call in newStreamChain — in-flight streams would not be " +
				"bound to the drain signal, so a drain could not cancel them.",
		})
	}
	// Part B anti-vacuity: the sole allowlisted firing site must host a live call.
	for f := range drainSignalTriggerCallerAllowlist {
		if _, seen := observedTrigger[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"GRPC-STREAM-DRAIN-01 (B): allowlist entry %q is STALE — no live "+
						"runtimegrpc.DrainSignal.Trigger call observed. The adapter no longer fires the drain, so "+
						"GracefulStop would wait on in-flight streams instead of canceling them; or the scanner "+
						"regressed. Drop or update the dead allowlist entry.",
					f,
				),
			})
		}
	}

	Report(t, "GRPC-STREAM-DRAIN-01", diags)
}

// ─── Single wiring funnel (#1752) ───────────────────────────────────────────────

// # GRPC-WIRING-REGISTRAR-MINT-FUNNEL-01
//
// runtime/grpc.NewServiceRegistrar and runtime/grpc.NewDrainSignal are the SOLE
// constructors of the two shared gRPC wiring singletons (their fields are
// unexported, so a non-zero value is unconstructable outside runtime/grpc). The
// cell-attribution chain reads the registrar's CellIDForMethod; the adapter binds
// the registrar to the *grpc.Server and triggers the drain at GracefulStop. For
// "the chain reads registrar A while the adapter binds registrar B → every RPC
// silently attributed to the runtime sentinel" to be unrepresentable, exactly one
// site may mint these in production: interceptor.NewServerInterceptors, which mints
// one of each and wires the SAME instances into both chains and the adapter bundle
// (#1752). Minting a second instance is one of two ways to reintroduce the
// mismatch; the other — recombining two already-minted bundles — is closed by the
// sibling GRPC-WIRING-BUNDLE-CALLER-01 below.
//
// This archtest pins the production caller identity of NewServiceRegistrar AND
// NewDrainSignal to:
//
//	{ runtime/grpc/interceptor/chain.go::NewServerInterceptors }
//
// ## AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - The same-instance guarantee itself is HARD-by-type-system, not enforced
//     here: interceptor.Deps carries no registrar/drain field and the chain
//     builders (newUnaryChain/newStreamChain) are package-private, so no external
//     code can compose a registrar-reading chain. Within NewServerInterceptors the
//     minted reg/drain feed both chains and the bundle, so the instances are
//     provably identical by construction. There is no AST shape to forbid for the
//     documented path — the type system forbids it before analysis.
//   - This archtest is the MEDIUM backstop on the registrar/drain SOURCE: the
//     sealed singletons are unconstructable except via these two functions, so
//     pinning their callers to the funnel means no production code can obtain a
//     SECOND registrar/drain to mismatch. (The recombination path — assembling a
//     fresh bundle from a registrar accessor-lifted off another bundle, which mints
//     nothing — is NOT caught here; it is caught by GRPC-WIRING-BUNDLE-CALLER-01.)
//     Downstream: MEDIUM by go/types caller-allowlist, resolved by SYMBOL via
//     collectGRPCWiringRefs — it covers BOTH the cross-package selector form
//     (ResolvePackageRef, with import aliases and function-value references
//     resolving to the same symbol) AND the bare-identifier form (TypesInfo.Uses):
//     a same-package call inside runtime/grpc itself, a dot-import, or a stored
//     function value all resolve to the target *types.Func and are flagged. Upstream:
//     HARD is UNREACHABLE — NewServiceRegistrar/NewDrainSignal are EXPORTED (the
//     interceptor package, a different package, must call them), and Go visibility
//     cannot express "only NewServerInterceptors may call this exported func". Same
//     permanent ceiling as CALLER-01 (#1394/#851/#1282).
//
// ## Tool blind spots (charter §"强制盲区自检")
//
//   - Same-package bare-identifier calls inside runtime/grpc itself (mint() instead
//     of runtimegrpc.mint()) ARE enforced: collectGRPCWiringRefs resolves bare
//     *ast.Ident references via TypesInfo.Uses, not just SelectorExpr. This closes
//     what would otherwise be the most reachable bypass — the guarded constructors
//     are DEFINED in runtime/grpc, so a same-package helper is exactly where a bare
//     call could appear (unlike CALLER-01, whose target is a third-party func that
//     can never be bare-called from this module). The bare-ident branch is exercised
//     by fixture_dotimport.go (a dot-import yields the same AST/types shape).
//   - Production scope only: _test.go callers are not scanned (Tests:false), so the
//     adapter-layer test seam (adapters/grpc/{server,readyz}_test.go, which mint +
//     NewServerInterceptorsBundle directly to avoid importing the interceptor
//     package per GRPC-ADAPTER-LAYER-01) is intentionally exempt — the same standard
//     Production-scope convention the CALLER-01 family relies on.
//   - The anti-vacuity reverse check (the sole allowlisted file must host a live
//     reference) proves the scanner resolves real references; the RED fixture
//     (TestArchtest_GRPCWiringRegistrarMintFunnel01_RedFixture) runs the SAME guard
//     and asserts it emits a violation Diagnostic per out-of-funnel mint (selector +
//     bare-ident) — proving the allowlist→Diagnostic branch fires, not merely that
//     the resolver sees the symbol.
//
// # GRPC-WIRING-BUNDLE-CALLER-01
//
// runtime/grpc.NewServerInterceptorsBundle is the SOLE constructor of a
// ServerInterceptors bundle (the value the adapter consumes: it binds the bundle's
// registrar and triggers the bundle's drain). The mint funnel above proves no
// production code can obtain a SECOND registrar/drain — but the bundle constructor
// is a second escape hatch the mint guard cannot see: a caller could lift a
// registrar/drain off an already-minted bundle via the b.Registrar()/b.Drain()
// accessors and assemble a FRESH bundle whose options were built from a different
// registrar (b1.ServerOptions() + b2.Registrar()). The chains then read b1's
// registrar while the adapter binds b2's → mismatch, with no mint call to catch.
// This archtest closes that path by pinning the production caller of
// NewServerInterceptorsBundle to:
//
//	{ runtime/grpc/interceptor/chain.go::NewServerInterceptors }
//
// ## AI-robust rating
//
//   - Downstream: MEDIUM by go/types caller-allowlist resolved by SYMBOL
//     (collectGRPCWiringRefs — selector + bare-ident, shared with the mint funnel).
//     Together with the mint funnel this is the closed funnel the charter requires
//     ("只锁 callsite 不是闭环 funnel"): the registrar/drain SOURCE is locked (mint
//     funnel) AND the bundle ASSEMBLY is locked (here), so neither a fresh mint nor
//     a recombination can produce a mismatched bundle in production — via selector
//     OR same-package bare call.
//   - Upstream: HARD is UNREACHABLE — NewServerInterceptorsBundle is EXPORTED
//     because interceptor (a different package) must call it; Go cannot seal its
//     callers. Same permanent ceiling as the rest of the CALLER family.
//
// ## Tool blind spots
//
//   - Same-package bare-identifier assembly inside runtime/grpc IS enforced (shared
//     collectGRPCWiringRefs bare-ident branch); exercised by fixture_dotimport.go.
//   - Production scope only (same _test.go exemption as the mint funnel: the
//     adapter test seam calls NewServerInterceptorsBundle directly by design).
//   - The RED fixture (TestArchtest_GRPCWiringBundleCaller01_RedFixture) runs the
//     SAME guard and asserts one violation Diagnostic per out-of-funnel assembly
//     (selector + bare-ident) — proving the allowlist→Diagnostic branch fires.

// grpcWiringFixturePkg is the RED fixture package exercising both reference forms
// (cross-package selector in fixture.go, bare-ident via dot-import in
// fixture_dotimport.go) from outside any allowlist.
const grpcWiringFixturePkg = "./tools/archtest/internal/grpcwiringmintfixture"

// grpcWiringRef is a resolved reference (selector OR bare ident) to a guarded
// runtime/grpc wiring constructor.
type grpcWiringRef struct {
	name string
	pos  token.Pos
}

// collectGRPCWiringRefs returns every reference in file to a runtime/grpc function
// whose name is in targets, resolved by SYMBOL via go/types. It covers BOTH forms:
//
//   - cross-package selector (runtimegrpc.NewServiceRegistrar) — ResolvePackageRef;
//   - bare identifier — a same-package call inside runtime/grpc itself, a dot-import,
//     or a function-value reference — resolved via TypesInfo.Uses.
//
// Resolving the function symbol (not just the selector form) closes the gap where a
// helper defined in runtime/grpc — the very package that owns these constructors —
// could bypass the caller-allowlist with a bare NewServiceRegistrar() call (#1752
// F1). The selector's .Sel ident is deduped so a cross-package call counts once.
func collectGRPCWiringRefs(info *types.Info, file *ast.File, targets map[string]struct{}) []grpcWiringRef {
	var refs []grpcWiringRef
	selSel := map[*ast.Ident]struct{}{}
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		selSel[sel.Sel] = struct{}{}
		pkgPath, name, ok := ResolvePackageRef(info, sel)
		if ok && pkgPath == grpcRuntimePkgPath {
			if _, t := targets[name]; t {
				refs = append(refs, grpcWiringRef{name: name, pos: sel.Pos()})
			}
		}
	})
	EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
		if _, isSel := selSel[id]; isSel {
			return // already counted as a selector's .Sel above
		}
		f, ok := info.Uses[id].(*types.Func)
		if !ok || f.Pkg() == nil || f.Pkg().Path() != grpcRuntimePkgPath {
			return
		}
		if _, t := targets[f.Name()]; !t {
			return
		}
		refs = append(refs, grpcWiringRef{name: f.Name(), pos: id.Pos()})
	})
	return refs
}

// grpcWiringGuard is a closed caller-allowlist over a set of runtime/grpc wiring
// constructors, resolved by symbol (selector + bare ident) via collectGRPCWiringRefs.
// The SAME guard runs in production (runProduction) and against the RED fixture
// (runRedFixture), so the fixture exercises the real allowlist→Diagnostic branch —
// not merely symbol resolution (#1752 F2). A fixture that only counted AST hits
// would stay green if the allowlist were widened or the diagnostic append deleted.
type grpcWiringGuard struct {
	ruleID    string
	targets   map[string]struct{}
	allowlist map[string]struct{}
	message   func(name, rel string) string // violation diagnostic for ref `name` in file `rel`
}

// scanFile returns (refCount, diagnostics) for one file: refCount counts guarded
// references (for the anti-vacuity check), diagnostics flags those outside the
// allowlist.
func (g grpcWiringGuard) scanFile(p *Pass, file *ast.File, rel string) (int, []Diagnostic) {
	refs := collectGRPCWiringRefs(p.TypesInfo, file, g.targets)
	if len(refs) == 0 {
		return 0, nil
	}
	if _, allowed := g.allowlist[rel]; allowed {
		return len(refs), nil
	}
	d := make([]Diagnostic, 0, len(refs))
	for _, ref := range refs {
		d = append(d, Diagnostic{
			Rel:     rel,
			Line:    p.Fset.Position(ref.pos).Line,
			Message: g.message(ref.name, rel),
		})
	}
	return len(refs), d
}

// runProduction scans all production files, flags out-of-allowlist references, and
// appends an anti-vacuity diagnostic if an allowlisted file hosts no live reference
// (a stale entry is a latent bypass slot).
func (g grpcWiringGuard) runProduction(t *testing.T) {
	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			n, fd := g.scanFile(p, file, rel)
			if n > 0 {
				observed[rel] = struct{}{}
			}
			d = append(d, fd...)
		}
		return d
	})
	for f := range g.allowlist {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"%s: allowlist entry %q is STALE — no live reference observed. Either the funnel moved "+
						"or the scanner regressed; drop or update the dead allowlist entry so it cannot become a "+
						"silent bypass slot.",
					g.ruleID, f),
			})
		}
	}
	Report(t, g.ruleID, diags)
}

// runRedFixture runs the SAME guard against the fixture package (whose path is not
// in the allowlist) and asserts it emits exactly wantDiags violation diagnostics,
// each located in the fixture and carrying the rule ID. This proves the
// allowlist→Diagnostic branch actually fires — for BOTH the selector and the
// bare-ident form (the fixture has both) — rather than only that the resolver sees
// the symbol.
func (g grpcWiringGuard) runRedFixture(t *testing.T, wantDiags int) {
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{grpcWiringFixturePkg}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			var d []Diagnostic
			for _, file := range p.Files {
				_, fd := g.scanFile(p, file, p.Rel(file))
				d = append(d, fd...)
			}
			return d
		})

	assert.Len(t, diags, wantDiags,
		"%s RED fixture self-check FAILED: expected exactly %d violation diagnostics from %s "+
			"(selector form in fixture.go + bare-ident form in fixture_dotimport.go); got %d. A miss "+
			"means the resolver or the allowlist→Diagnostic branch regressed — the fixture runs the SAME "+
			"guard the production rule runs.",
		g.ruleID, wantDiags, grpcWiringFixturePkg, len(diags))
	for _, d := range diags {
		assert.Contains(t, d.Rel, "grpcwiringmintfixture",
			"%s RED fixture diagnostic must point at the fixture, got %q", g.ruleID, d.Rel)
		assert.Contains(t, d.Message, g.ruleID,
			"%s RED fixture diagnostic must carry the rule ID", g.ruleID)
	}
}

// grpcWiringMintGuard pins production callers of the registrar/drain constructors
// (the sealed-singleton SOURCE) to the sole funnel NewServerInterceptors in chain.go.
var grpcWiringMintGuard = grpcWiringGuard{
	ruleID:    "GRPC-WIRING-REGISTRAR-MINT-FUNNEL-01",
	targets:   map[string]struct{}{"NewServiceRegistrar": {}, "NewDrainSignal": {}},
	allowlist: map[string]struct{}{"runtime/grpc/interceptor/chain.go": {}}, // NewServerInterceptors — sole mint funnel
	message: func(name, rel string) string {
		return fmt.Sprintf(
			"GRPC-WIRING-REGISTRAR-MINT-FUNNEL-01: runtimegrpc.%s is referenced from %s, which is not the "+
				"sanctioned gRPC wiring funnel. Minting a registrar/drain anywhere but "+
				"runtime/grpc/interceptor.NewServerInterceptors lets a composition root hold two instances and "+
				"wire a chain that reads one while the adapter binds the other (#1752: every RPC silently "+
				"attributed to the runtime sentinel). Obtain the wiring bundle from NewServerInterceptors(deps) "+
				"instead. If this IS a new sanctioned funnel, add it to grpcWiringMintGuard.allowlist with rationale.",
			name, rel)
	},
}

// grpcWiringBundleGuard pins production callers of NewServerInterceptorsBundle (the
// bundle ASSEMBLY point) to chain.go, closing the recombination path the mint guard
// cannot see (a bundle assembled from a registrar lifted off another bundle).
var grpcWiringBundleGuard = grpcWiringGuard{
	ruleID:    "GRPC-WIRING-BUNDLE-CALLER-01",
	targets:   map[string]struct{}{"NewServerInterceptorsBundle": {}},
	allowlist: map[string]struct{}{"runtime/grpc/interceptor/chain.go": {}}, // NewServerInterceptors — sole bundle assembler
	message: func(_, rel string) string {
		return fmt.Sprintf(
			"GRPC-WIRING-BUNDLE-CALLER-01: runtimegrpc.NewServerInterceptorsBundle is referenced from %s, "+
				"which is not the sanctioned bundle assembler. Assembling a ServerInterceptors bundle anywhere "+
				"but runtime/grpc/interceptor.NewServerInterceptors lets a caller recombine a registrar/drain "+
				"(e.g. lifted off another bundle via b.Registrar()) with options built from a DIFFERENT registrar "+
				"— a mismatch the mint funnel cannot see (#1752). Obtain the bundle from NewServerInterceptors(deps) "+
				"instead. If this IS a new sanctioned assembler, add it to grpcWiringBundleGuard.allowlist with rationale.",
			rel)
	},
}

// TestArchtest_GRPCWiringRegistrarMintFunnel01 pins production callers of
// NewServiceRegistrar / NewDrainSignal (selector + bare-ident) to the funnel.
func TestArchtest_GRPCWiringRegistrarMintFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	grpcWiringMintGuard.runProduction(t)
}

// TestArchtest_GRPCWiringRegistrarMintFunnel01_RedFixture asserts the guard emits a
// violation diagnostic for each out-of-funnel mint in the fixture: 2 selector
// (badMint) + 2 bare-ident (badMintBareIdent) = 4.
func TestArchtest_GRPCWiringRegistrarMintFunnel01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	grpcWiringMintGuard.runRedFixture(t, 4)
}

// TestArchtest_GRPCWiringBundleCaller01 pins production callers of
// NewServerInterceptorsBundle (selector + bare-ident) to the funnel.
func TestArchtest_GRPCWiringBundleCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	grpcWiringBundleGuard.runProduction(t)
}

// TestArchtest_GRPCWiringBundleCaller01_RedFixture asserts the guard emits a
// violation diagnostic for each out-of-funnel bundle assembly in the fixture: 1
// selector (badBundle) + 1 bare-ident (badBundleBareIdent) = 2.
func TestArchtest_GRPCWiringBundleCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	grpcWiringBundleGuard.runRedFixture(t, 2)
}
