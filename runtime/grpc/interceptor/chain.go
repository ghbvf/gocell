package interceptor

import (
	"google.golang.org/grpc"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// Deps holds the dependencies the unary interceptor chain needs. They are
// supplied by the composition root (cmd/ or examples/); this package only
// composes them.
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
	// CellResolver maps fullMethod→cellID for the cell-attribution interceptor
	// (Option 3, #1152). The composition root supplies the registrar's
	// CellIDForMethod. Required: a nil resolver is a wiring bug → NewUnaryChain
	// panics, rather than leaving every RPC attributed to the runtime sentinel.
	CellResolver CellResolver
	// CellIDClosedSet is the assembly's cell-id set (asm.CellIDs()) the metrics
	// interceptor validates the attributed cell against (M12b defense-in-depth: an
	// out-of-set cell degrades to the runtime sentinel, never pollutes the SLO
	// series). Required + non-empty: a half-wired chain (resolver set, closed set
	// empty) would relabel every cell _runtime — NewUnaryChain fails fast instead.
	CellIDClosedSet []string
}

// NewUnaryChain composes the unary interceptors into a single grpc.ServerOption.
// This is the single authoritative composition point; the order is fixed here
// (RequestID outermost, Recovery innermost) and guarded by archtest
// GRPC-INTERCEPTOR-CHAIN-ORDER-01. See the package doc for the order rationale.
//
// CellResolver and CellIDClosedSet are required (fail-closed): a chain composed
// without them would silently relabel every RPC to the runtime sentinel.
func NewUnaryChain(deps Deps) grpc.ServerOption {
	if deps.CellResolver == nil {
		panic(panicregister.Approved("interceptor-chain-cell-resolver-required",
			errcode.Assertion("interceptor.NewUnaryChain: Deps.CellResolver is required")))
	}
	if len(deps.CellIDClosedSet) == 0 {
		panic(panicregister.Approved("interceptor-chain-cell-closed-set-required",
			errcode.Assertion(
				"interceptor.NewUnaryChain: Deps.CellIDClosedSet is required (the assembly cell-id set)")))
	}
	validCellIDs := make(map[string]struct{}, len(deps.CellIDClosedSet))
	for _, id := range deps.CellIDClosedSet {
		validCellIDs[id] = struct{}{}
	}
	return grpc.ChainUnaryInterceptor(
		UnaryRequestID(),
		UnaryCellAttribution(deps.CellResolver),
		UnaryTracing(deps.Tracer),
		UnaryAccessLog(deps.Clock),
		UnaryMetrics(deps.Collector, deps.Clock, validCellIDs),
		UnaryAuth(deps.Verifier, deps.AuthOptions...),
		UnaryRecovery(),
	)
}
