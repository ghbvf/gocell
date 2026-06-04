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

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/validation"
)

// GRPCServiceRegistrar is a local alias for [cell.GRPCServiceRegistrar] (the
// canonical definition in kernel/cell/grpc_service.go). The alias lets the
// bootstrap-package [GRPCServer] interface and bootstrap-package tests name the
// type without an additional import path; the canonical definition lives in
// kernel/cell so both adapters/grpc and bootstrap can import it without an
// import cycle.
type GRPCServiceRegistrar = cell.GRPCServiceRegistrar

// GRPCServer is the narrow lifecycle contract bootstrap needs from a gRPC
// server. adapters/grpc.Server satisfies it (Serve serves a pre-bound listener;
// Close gracefully drains in-flight RPCs bounded by the passed ctx; Registrar
// returns the cell-facing service registrar used by the drain in phase7b).
// Defining it here — rather than importing the adapter — keeps runtime/bootstrap
// free of any adapters/ dependency (LAYER-03 / GRPC-ADAPTER-LAYER-01).
type GRPCServer interface {
	// Serve serves RPCs on the pre-bound listener until ctx is canceled or the
	// server stops (e.g. via Close). It blocks; bootstrap calls it in a goroutine.
	Serve(ctx context.Context, lis net.Listener) error
	// Close initiates a graceful drain of in-flight RPCs bounded by ctx, hard
	// stopping if the budget is exceeded. Idempotent.
	Close(ctx context.Context) error
	// Registrar returns the cell-facing service registrar. bootstrap calls
	// Registrar().Register(spec) in phase7b for each GRPCServiceSpec drained from
	// the cell snapshots, before grpcServeAll.
	Registrar() GRPCServiceRegistrar
}

// grpcListenerConfig is the resolved per-gRPC-listener wiring captured by
// WithGRPCListener and consumed by phase7b (serve) + phase10 stage2 (drain).
type grpcListenerConfig struct {
	ref       cell.ListenerRef // identity for routing cell GRPCServiceSpecs (GAP-1 PR-7)
	server    GRPCServer
	addr      string
	net       net.Listener  // optional pre-bound socket (bufconn/test); nil → bootstrap binds addr
	shutGrace time.Duration // optional per-listener drain budget; 0 → inherit global shutdownTimeout
}

// GRPCListenerOption configures a single gRPC listener within WithGRPCListener.
type GRPCListenerOption func(*grpcListenerConfig)

// WithGRPCListenerNet injects a pre-bound net.Listener (e.g. a bufconn listener
// in tests, or a TCP socket bound by the caller). When set, addr is used only
// for logging. nil is stored as-is; phase7b then binds addr via net.Listen.
func WithGRPCListenerNet(ln net.Listener) GRPCListenerOption {
	return func(c *grpcListenerConfig) { c.net = ln }
}

// WithGRPCListenerShutdownGrace sets the per-listener drain budget passed to
// GRPCServer.Close in phase10 stage2. Zero inherits the global shutdownTimeout;
// a negative value is rejected at phase0 (ErrCellInvalidConfig). Named for
// symmetry with the HTTP WithListenerShutdownGrace.
func WithGRPCListenerShutdownGrace(d time.Duration) GRPCListenerOption {
	return func(c *grpcListenerConfig) { c.shutGrace = d }
}

// WithGRPCListener declares a gRPC listener served on addr by the
// composition-root-constructed server (GAP-1 PR-7 [#1150]).
//
// ref identifies this listener so that cells can route their GRPCServiceSpecs
// to the correct server via GRPCServiceSpec.Listener — fully symmetric with
// HTTP's WithListener(ref, addr, authChain). ref must be non-zero and unique
// across all WithGRPCListener calls; a zero or duplicate ref is rejected at
// phase0 (validateGRPCListenerConfigs), mirroring the HTTP listener ref gate,
// before any socket binds.
//
// The server must be non-nil — both bare-nil and typed-nil are rejected at
// phase0 with ErrGRPCServerMissing (mirroring WithRateLimiter / WithManagedResource).
// bootstrap drives Serve in phase7b (in parallel with HTTP) and Close in
// phase10 stage2 (before LIFO teardown, so in-flight RPCs drain while backends
// are alive).
//
// Composition-root wiring (cmd/ or examples/, which may import adapters/grpc and
// runtime/grpc/interceptor — cells/ may not):
//
//	chain := interceptor.NewUnaryChain(interceptor.Deps{
//	    Verifier: verifier, Clock: clk, Collector: collector, Tracer: tracer,
//	}) // always wires UnaryAuth; panics on a nil verifier (fail-closed)
//	srv, err := adaptersgrpc.New(adaptersgrpc.Config{
//	    Addr: ":9000", TLS: tlsCfg,
//	    ServerOptions: []grpc.ServerOption{chain},
//	})
//	bootstrap.New(clk, bootstrap.WithGRPCListener(cell.PrimaryListener, srv, ":9000"))
//
// Cell services are drained from RegistrySnapshot.GRPCServices in phase7b
// (after binding, before grpcServeAll) and registered via
// server.Registrar().Register(spec) — the stopgap of pre-passing registered
// services is replaced by this drain.
func WithGRPCListener(ref cell.ListenerRef, server GRPCServer, addr string, opts ...GRPCListenerOption) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(server) {
			b.grpcServerNil = true // sentinel → phase0 fail-fast
			return
		}
		cfg := grpcListenerConfig{ref: ref, server: server, addr: addr}
		for _, o := range opts {
			o(&cfg)
		}
		b.grpcListenerConfigs = append(b.grpcListenerConfigs, cfg)
	}
}
