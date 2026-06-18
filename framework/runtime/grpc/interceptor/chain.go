package interceptor

import (
	"google.golang.org/grpc"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	runtimegrpc "github.com/ghbvf/gocell/framework/runtime/grpc"
	"github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// Deps holds the dependencies the unary AND streaming interceptor chains need
// (newUnaryChain / newStreamChain share one Deps). They are supplied by the
// composition root (cmd/ or examples/); this package only composes them.
//
// Deliberately ABSENT: the cell-attribution ServiceRegistrar and the stream
// DrainSignal. Those are minted by NewServerInterceptors itself (#1752) — one of
// each, wired into both chains AND the adapter bundle — so a composition root can
// no longer hold (and therefore can no longer mismatch) two registrars/drains.
// The "chain reads registrar A while the adapter binds registrar B → every RPC
// silently attributed to the runtime sentinel" escape hatch is unrepresentable:
// there is no field through which a registrar/drain enters this struct, and the
// chain builders are package-private so no external code can compose a
// registrar-reading chain at all.
type Deps struct {
	// Tracer records one span per RPC. A nil Tracer degrades to NoopTracer.
	Tracer wrapper.Tracer
	// Collector records grpc_server_* metrics.
	Collector metrics.GRPCCollector
	// Clock times request duration for the metrics + access-log interceptors (required).
	Clock clock.Clock
	// Verifier authenticates bearer tokens (required; nil fails closed).
	Verifier auth.IntentTokenVerifier
	// Authorizer is the ABAC PDP the per-method permission gate consults (#2008),
	// the gRPC analog of bootstrap.WithPrimaryAuthorizer for HTTP. The composition
	// root supplies the same cell-provided Authorizer it wires into the HTTP primary
	// listener so gRPC method authorization is the identical decision.
	//
	// nil is permitted ONLY when no cell declares permission-gated methods. NewServerInterceptors
	// threads whether this is non-nil into the minted registrar (WithPermissionGate); if any
	// registered spec carries endpoints.grpc.methods[].permission while this is nil, Register
	// fail-fasts at startup (phase7b drain, after Init, before Serve) — parity with HTTP's
	// bootstrap.ResolveAuthorizer pre-serve guard (#2008 F1). A gated method with no Authorizer
	// no longer boots-and-403s-at-request-time; the wiring bug surfaces at boot.
	Authorizer auth.Authorizer
	// MetricsProvider is the assembly's metrics backend. When it is a REAL provider
	// (kernelmetrics.IsReal — non-nil, non-Nop) NewServerInterceptors wraps Authorizer in
	// auth.NewObservableAuthorizer so every gRPC PDP decision is counted + timed under the same
	// auth_pdp_decision_* series HTTP uses (#2008 F8 — transport parity; the metric family is
	// shared via the provider's registerOrReuse, no transport label, no double-registration).
	// A Nop/nil provider leaves Authorizer bare (metrics are best-effort and never gate the
	// verdict), mirroring the HTTP bootstrap hasRealMetricsProvider gate.
	MetricsProvider kernelmetrics.Provider
	// AuthOptions configures the auth interceptor (public-method and
	// password-reset-exempt predicates).
	AuthOptions []AuthOption
	// CellIDClosedSet is the assembly's cell-id set (asm.CellIDs()) the metrics
	// interceptor validates the attributed cell against (M12b defense-in-depth: an
	// out-of-set cell degrades to the runtime sentinel, never pollutes the SLO
	// series). Required + non-empty: a half-wired chain (resolver set, closed set
	// empty) would relabel every cell _runtime — the chain builders fail fast instead.
	CellIDClosedSet []string
}

// newUnaryChain composes the unary interceptors into a single grpc.ServerOption.
// It is package-private: NewServerInterceptors is the sole caller and supplies the
// shared registrar it mints (#1752), so no external code can compose a chain that
// reads a registrar other than the one the adapter binds. The order is fixed here
// (RequestID outermost, Recovery innermost) and guarded by archtest
// GRPC-INTERCEPTOR-CHAIN-ORDER-01. See the package doc for the order rationale.
//
// reg and CellIDClosedSet are required (fail-closed): a chain composed without
// them would silently relabel every RPC to the runtime sentinel.
func newUnaryChain(deps Deps, reg *runtimegrpc.ServiceRegistrar) grpc.ServerOption {
	if reg == nil {
		panic(panicregister.Approved("interceptor-chain-registrar-required",
			errcode.Assertion("interceptor.newUnaryChain: registrar is required")))
	}
	if len(deps.CellIDClosedSet) == 0 {
		panic(panicregister.Approved("interceptor-chain-cell-closed-set-required",
			errcode.Assertion(
				"interceptor.newUnaryChain: Deps.CellIDClosedSet is required (the assembly cell-id set)")))
	}
	validCellIDs := buildValidCellIDs(deps.CellIDClosedSet)
	return grpc.ChainUnaryInterceptor(
		UnaryRequestID(),
		UnaryCellAttribution(reg.CellIDForMethod),
		UnaryTracing(deps.Tracer),
		UnaryAccessLog(deps.Clock),
		UnaryMetrics(deps.Collector, deps.Clock, validCellIDs),
		UnaryAuth(deps.Verifier, authChainOptions(deps, reg)...),
		UnaryRecovery(),
	)
}

// authChainOptions returns the composition-root AuthOptions with the
// registrar/authorizer-sourced predicates added. The registrar is the SINGLE
// runtime source of ALL FOUR gRPC auth dimensions:
//
//   - public-method bypass (#1675): WithPublicMethod(reg.IsPublicMethod), derived
//     from each cell's endpoints.grpc.methods[] (public:true). Guarded by
//     GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01.
//   - per-method permission gate (#2008): WithPermissionResolver(reg.PermissionForMethod),
//     derived from endpoints.grpc.methods[].permission, plus WithPDPAuthorizer(deps.Authorizer)
//     — the cell-provided PDP, the gRPC analog of bootstrap.WithPrimaryAuthorizer.
//     Guarded by GRPC-PERMISSION-GATE-WIRING-FUNNEL-01.
//   - owner-scoped resource extraction (#2207): WithResourceResolver(reg.ResourceFieldForMethod),
//     derived from endpoints.grpc.methods[].resource — the field whose value is
//     extracted from the first received message and forwarded as the PDP resource.
//     Guarded by GRPC-METHOD-RESOURCE-FIELD-FUNNEL-01.
//   - password-reset-exempt bypass (#1382): WithPasswordResetExempt(reg.IsPasswordResetExemptMethod),
//     derived from endpoints.grpc.methods[].passwordResetExempt — exempt methods are
//     non-public and still require a permission (the gate is orthogonal to ABAC).
//     Guarded by GRPC-PASSWORD-RESET-EXEMPT-WIRING-FUNNEL-01.
//
// chain.go is the SOLE production installer of all five options; the funnel archtests
// forbid any other production reference, so each composed source has exactly one
// member in production: the registrar/authorizer. Test harnesses may add synthetic
// options via deps.AuthOptions (allowed only in _test.go). A fresh slice is returned
// so the unary and stream chains (sharing one Deps) never alias-append into the same
// backing array.
func authChainOptions(deps Deps, reg *runtimegrpc.ServiceRegistrar) []AuthOption {
	out := make([]AuthOption, 0, len(deps.AuthOptions)+5)
	out = append(out, deps.AuthOptions...)
	out = append(out,
		WithPublicMethod(reg.IsPublicMethod),
		WithPermissionResolver(reg.PermissionForMethod),
		WithPDPAuthorizer(deps.Authorizer),
		WithResourceResolver(reg.ResourceFieldForMethod),
		WithPasswordResetExempt(reg.IsPasswordResetExemptMethod),
	)
	return out
}

// NewServerInterceptors returns the adapter-consumable bundle for a complete
// GoCell gRPC server. It mints the ONE shared ServiceRegistrar and the ONE shared
// DrainSignal, builds both the unary and streaming chains from them, and carries
// the same instances for the adapter to bind and trigger.
//
// This is the SOLE composition-root entrypoint AND the sole production minter of
// the registrar/drain (GRPC-WIRING-REGISTRAR-MINT-FUNNEL-01). Because the same
// reg/drain feed both chains and the bundle within this one function, the
// "compile-proof same-instance" guarantee holds by construction (#1752): cell
// attribution and service registration provably share one method map, and stream
// drain provably binds the signal the adapter triggers — a caller cannot supply
// two. It also closes the "forgot newStreamChain" streaming-auth gap while keeping
// adapters/grpc free of a direct interceptor import.
func NewServerInterceptors(deps Deps) runtimegrpc.ServerInterceptors {
	// F1 (#2008): capture whether a PDP Authorizer backs the gate BEFORE the F8
	// wrap (the observable decorator is always non-nil, so reading after would mask
	// a nil source). The minted registrar carries this bit so Register fail-fasts
	// at startup if a permission-gated spec is registered with no Authorizer.
	permissionGateWired := !validation.IsNilInterface(deps.Authorizer)

	// F8 (#2008): wrap the PDP Authorizer with decision metrics when a real metrics
	// provider is configured, so gRPC PDP decisions reach the same auth_pdp_decision_*
	// series as HTTP (transport parity). Centralized here — every gRPC cell gets it,
	// not per-composition-root. Skipped when there is no Authorizer (nothing to gate)
	// or no real provider (metrics are best-effort, never gate the verdict). The metric
	// family is shared with the HTTP path via the provider's registerOrReuse (same name +
	// labels), so wrapping in both transports does not double-register.
	if permissionGateWired && kernelmetrics.IsReal(deps.MetricsProvider) {
		pdpMetrics, err := auth.NewPDPMetrics(deps.MetricsProvider)
		if err != nil {
			panic(panicregister.Approved("grpc-interceptor-pdp-metrics",
				errcode.Assertion(
					"interceptor.NewServerInterceptors: register PDP decision metrics: %v", err)))
		}
		deps.Authorizer = auth.NewObservableAuthorizer(deps.Clock, deps.Authorizer, pdpMetrics)
	}

	reg := runtimegrpc.NewServiceRegistrar(runtimegrpc.WithPermissionGate(permissionGateWired))
	drain := runtimegrpc.NewDrainSignal()
	return runtimegrpc.NewServerInterceptorsBundle(
		[]grpc.ServerOption{
			newUnaryChain(deps, reg),
			newStreamChain(deps, reg, drain),
		},
		reg,
		drain,
	)
}

// buildValidCellIDs materializes the assembly cell-id closed set into the lookup
// map the metrics interceptors validate the attributed cell against. Shared by
// newUnaryChain and newStreamChain (PR-10 #1153).
func buildValidCellIDs(ids []string) map[string]struct{} {
	validCellIDs := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		validCellIDs[id] = struct{}{}
	}
	return validCellIDs
}
