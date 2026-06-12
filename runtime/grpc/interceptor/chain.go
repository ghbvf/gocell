package interceptor

import (
	"google.golang.org/grpc"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/runtime/auth"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
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
		UnaryAuth(deps.Verifier, deps.AuthOptions...),
		UnaryRecovery(),
	)
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
	reg := runtimegrpc.NewServiceRegistrar()
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
