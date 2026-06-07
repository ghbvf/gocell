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
// (NewUnaryChain / NewStreamChain share one Deps). They are supplied by the
// composition root (cmd/ or examples/); this package only composes them. Drain is
// consumed only by the stream chain (StreamDrain); the unary chain ignores it.
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
	// Registrar is the gRPC service registrar whose CellIDForMethod feeds the
	// cell-attribution interceptor (Option 3, #1152). It MUST be the SAME
	// *runtimegrpc.ServiceRegistrar instance passed to adaptersgrpc.Config.Registrar
	// — a different registrar populates a different method map and silently degrades
	// attribution to _runtime. Taking the registrar object (not a detached
	// CellIDForMethod func value) makes the two consumers symmetric — both wire
	// `Registrar: reg` — so a mismatch is visible. (A compile-proof single-builder
	// that emits both the chain option and the adapter config is the Hard upgrade,
	// tracked at #1752.) Required: a nil Registrar is a wiring bug → NewUnaryChain
	// panics rather than leaving every RPC attributed to the sentinel.
	Registrar *runtimegrpc.ServiceRegistrar
	// CellIDClosedSet is the assembly's cell-id set (asm.CellIDs()) the metrics
	// interceptor validates the attributed cell against (M12b defense-in-depth: an
	// out-of-set cell degrades to the runtime sentinel, never pollutes the SLO
	// series). Required + non-empty: a half-wired chain (resolver set, closed set
	// empty) would relabel every cell _runtime — NewUnaryChain fails fast instead.
	CellIDClosedSet []string
	// Drain is the framework-side gRPC drain signal (PR-10 #1153) the StreamDrain
	// interceptor binds each in-flight stream's context to, so GracefulStop
	// actively cancels long-lived streams instead of merely waiting for them. It
	// MUST be the SAME *runtimegrpc.DrainSignal instance passed to
	// adaptersgrpc.Config.Drain — the adapter triggers it at GracefulStop start
	// and the stream chain observes the cancellation (same Option-3 instance
	// symmetry as Registrar; the single-builder Hard upgrade is #1752). Required
	// by NewStreamChain (fail-closed); NewUnaryChain ignores it (unary RPCs are
	// short-lived, GracefulStop's wait suffices).
	Drain *runtimegrpc.DrainSignal
}

// NewUnaryChain composes the unary interceptors into a single grpc.ServerOption.
// This is the single authoritative composition point; the order is fixed here
// (RequestID outermost, Recovery innermost) and guarded by archtest
// GRPC-INTERCEPTOR-CHAIN-ORDER-01. See the package doc for the order rationale.
//
// Registrar and CellIDClosedSet are required (fail-closed): a chain composed
// without them would silently relabel every RPC to the runtime sentinel.
func NewUnaryChain(deps Deps) grpc.ServerOption {
	if deps.Registrar == nil {
		panic(panicregister.Approved("interceptor-chain-registrar-required",
			errcode.Assertion("interceptor.NewUnaryChain: Deps.Registrar is required")))
	}
	if len(deps.CellIDClosedSet) == 0 {
		panic(panicregister.Approved("interceptor-chain-cell-closed-set-required",
			errcode.Assertion(
				"interceptor.NewUnaryChain: Deps.CellIDClosedSet is required (the assembly cell-id set)")))
	}
	// deps.Drain is intentionally NOT validated here: it is a stream-only concern
	// (StreamDrain) and the unary chain never reads it. A unary-only server may
	// leave it nil; the asymmetry with NewStreamChain (which requires it) is by
	// design — see the Deps.Drain field doc.
	validCellIDs := buildValidCellIDs(deps.CellIDClosedSet)
	return grpc.ChainUnaryInterceptor(
		UnaryRequestID(),
		UnaryCellAttribution(deps.Registrar.CellIDForMethod),
		UnaryTracing(deps.Tracer),
		UnaryAccessLog(deps.Clock),
		UnaryMetrics(deps.Collector, deps.Clock, validCellIDs),
		UnaryAuth(deps.Verifier, deps.AuthOptions...),
		UnaryRecovery(),
	)
}

// buildValidCellIDs materializes the assembly cell-id closed set into the lookup
// map the metrics interceptors validate the attributed cell against. Shared by
// NewUnaryChain and NewStreamChain (PR-10 #1153).
func buildValidCellIDs(ids []string) map[string]struct{} {
	validCellIDs := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		validCellIDs[id] = struct{}{}
	}
	return validCellIDs
}
