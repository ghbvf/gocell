package bootstrap

// grpc_listener.go — WithGRPCListener option + GRPCServer interface (GAP-1 PR-5).
//
// Layering: runtime/bootstrap must NOT import adapters/grpc (LAYER-03 strict
// allow-list). The gRPC server lifecycle lives in adapters/grpc; bootstrap only
// orchestrates serve + drain through the GRPCServer interface, which
// adapters/grpc.Server satisfies structurally. The composition root (cmd/,
// examples/) builds the interceptor chain (runtime/grpc/interceptor.NewUnaryChain,
// which always wires UnaryAuth and panics on a nil verifier) and constructs the
// adapter server with that chain injected via adaptersgrpc.Config.ServerOptions,
// then hands the ready server to WithGRPCListener. This keeps gRPC auth wiring in
// the composition root (AUTH-PLAN-04) and the server implementation single-sourced
// in the adapter.

import (
	"context"
	"net"
	"time"

	"github.com/ghbvf/gocell/pkg/validation"
)

// GRPCServer is the narrow lifecycle contract bootstrap needs from a gRPC
// server. adapters/grpc.Server satisfies it (Serve serves a pre-bound listener;
// Close gracefully drains in-flight RPCs bounded by the passed ctx). Defining it
// here — rather than importing the adapter — keeps runtime/bootstrap free of any
// adapters/ dependency (LAYER-03 / GRPC-ADAPTER-LAYER-01).
type GRPCServer interface {
	// Serve serves RPCs on the pre-bound listener until ctx is canceled or the
	// server stops (e.g. via Close). It blocks; bootstrap calls it in a goroutine.
	Serve(ctx context.Context, lis net.Listener) error
	// Close initiates a graceful drain of in-flight RPCs bounded by ctx, hard
	// stopping if the budget is exceeded. Idempotent.
	Close(ctx context.Context) error
}

// grpcListenerConfig is the resolved per-gRPC-listener wiring captured by
// WithGRPCListener and consumed by phase7b (serve) + phase10 stage2 (drain).
type grpcListenerConfig struct {
	server      GRPCServer
	addr        string
	net         net.Listener  // optional pre-bound socket (bufconn/test); nil → bootstrap binds addr
	shutTimeout time.Duration // optional per-listener drain budget; 0 → inherit global shutdownTimeout
}

// GRPCListenerOption configures a single gRPC listener within WithGRPCListener.
type GRPCListenerOption func(*grpcListenerConfig)

// WithGRPCListenerNet injects a pre-bound net.Listener (e.g. a bufconn listener
// in tests, or a TCP socket bound by the caller). When set, addr is used only
// for logging. nil is stored as-is; phase7b then binds addr via net.Listen.
func WithGRPCListenerNet(ln net.Listener) GRPCListenerOption {
	return func(c *grpcListenerConfig) { c.net = ln }
}

// WithGRPCListenerShutdownTimeout sets the per-listener drain budget passed to
// GRPCServer.Close in phase10 stage2. Zero inherits the global shutdownTimeout;
// a negative value is stored as-is and rejected at phase0.
func WithGRPCListenerShutdownTimeout(d time.Duration) GRPCListenerOption {
	return func(c *grpcListenerConfig) { c.shutTimeout = d }
}

// WithGRPCListener declares a gRPC listener served on addr by the
// composition-root-constructed server. The server must be non-nil — both
// bare-nil and typed-nil are rejected at phase0 with ErrGRPCServerMissing
// (strong-dependency wiring option, mirroring WithRateLimiter / WithManagedResource).
//
// The server is expected to already have its interceptor chain wired (via
// adaptersgrpc.Config.ServerOptions = []grpc.ServerOption{interceptor.NewUnaryChain(...)})
// and its services registered (via ServiceRegistrar) before being passed here.
// bootstrap drives Serve in phase7b (in parallel with HTTP) and Close in phase10
// stage2 (before LIFO teardown, so in-flight RPCs drain while backends are alive).
func WithGRPCListener(server GRPCServer, addr string, opts ...GRPCListenerOption) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(server) {
			b.grpcServerNil = true // sentinel → phase0 fail-fast
			return
		}
		cfg := grpcListenerConfig{server: server, addr: addr}
		for _, o := range opts {
			o(&cfg)
		}
		b.grpcListenerConfigs = append(b.grpcListenerConfigs, cfg)
	}
}
