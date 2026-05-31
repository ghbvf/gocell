package interceptor

import (
	"google.golang.org/grpc"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// Deps holds the dependencies the unary interceptor chain needs. They are
// supplied by the composition root (bootstrap, a later PR); this package only
// composes them.
type Deps struct {
	// Tracer records one span per RPC. A nil Tracer degrades to NoopTracer.
	Tracer wrapper.Tracer
	// Collector records grpc_server_* metrics.
	Collector metrics.GRPCCollector
	// Clock times request duration for the metrics interceptor (required).
	Clock clock.Clock
	// Verifier authenticates bearer tokens (required; nil fails closed).
	Verifier auth.IntentTokenVerifier
	// AuthOptions configures the auth interceptor (public-method and
	// password-reset-exempt predicates).
	AuthOptions []AuthOption
}

// NewUnaryChain composes the unary interceptors into a single grpc.ServerOption.
// This is the single authoritative composition point; the order is fixed here
// (RequestID outermost, Recovery innermost) and guarded by archtest
// GRPC-INTERCEPTOR-CHAIN-ORDER-01. See the package doc for the order rationale.
func NewUnaryChain(deps Deps) grpc.ServerOption {
	return grpc.ChainUnaryInterceptor(
		UnaryRequestID(),
		UnaryTracing(deps.Tracer),
		UnaryMetrics(deps.Collector, deps.Clock),
		UnaryAuth(deps.Verifier, deps.AuthOptions...),
		UnaryRecovery(),
	)
}
