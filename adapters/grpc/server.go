package grpc

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
	"github.com/ghbvf/gocell/runtime/http/tlsutil"
)

// ProbeReady is the readiness probe name for the gRPC server, funneled through
// the PROBENAME-SEALED-FUNNEL-01 typed-const discipline. The probe reflects
// whether the server is actively serving (grpcServer.Serve has started and not
// yet returned). The "_ready" suffix follows the adapter dependency-availability
// naming convention from .claude/rules/gocell/observability.md.
const ProbeReady healthz.ProbeName = "grpc_ready"

// Compile-time assertions: Server satisfies lifecycle.ManagedResource and
// serverWorker satisfies worker.Worker.
var (
	_ lifecycle.ManagedResource = (*Server)(nil)
	_ worker.Worker             = (*serverWorker)(nil)
)

// Server is a gRPC server adapter implementing lifecycle.ManagedResource.
// It wraps a *grpc.Server with the GoCell lifecycle contract:
// Probes() / Worker() / Close().
//
// Construction: call New(cfg) to obtain a configured *Server. Services are
// registered during bootstrap via Registrar().Register(spec) — cells declare
// GRPCServiceSpec in Cell.Init; bootstrap drains the snapshot and calls
// Registrar().Register for each spec in phase7b, before grpcServeAll.
//
// Concurrency: all public methods are safe for concurrent use. GracefulStop
// is idempotent via stopOnce — calling Worker().Stop() and Close() simultaneously
// (LIFO teardown) is safe.
type Server struct {
	cfg        Config
	grpcServer *grpc.Server
	registrar  *runtimegrpc.ServiceRegistrar

	// serving is true while grpcServer.Serve is actively running.
	// Flipped to true inside serve() after Serve returns from the initial
	// listen call, and back to false in gracefulStop() after drain completes.
	serving atomic.Bool

	// stopOnce ensures gracefulStop body runs exactly once regardless of how
	// many callers (Worker.Stop vs Close) trigger it concurrently.
	stopOnce sync.Once

	// serveDone is closed when grpcServer.Serve returns (normal or error path).
	// gracefulStop selects on this to avoid burning shutdown budget after Serve
	// has already exited.
	serveDone chan struct{}

	// serveErr holds the error returned by grpcServer.Serve, written before
	// serveDone is closed and read after it.
	serveErr error
}

// New creates a Server from cfg. It validates the config, builds TLS
// credentials (if configured), and constructs the underlying *grpc.Server.
// No network I/O is performed; binding happens when the Worker starts.
func New(cfg Config) (*Server, error) {
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	creds, err := buildCredentials(cfg.TLS)
	if err != nil {
		return nil, err
	}

	// Interceptor bundles are built outside the adapter (normally via
	// interceptor.NewServerInterceptors) to preserve GRPC-ADAPTER-LAYER-01. The
	// adapter consumes the bundle atomically: options, registrar, and drain all
	// come from the same value.
	opts := cfg.Interceptors.ServerOptions()
	// The adapter-owned transport credentials are appended LAST so they win.
	// grpc.NewServer applies options in order and a later grpc.Creds overrides an
	// earlier one, so the validated TLS/mTLS posture remains non-overridable.
	// ref: grpc-go v1.81.1 server.go NewServer + Creds.
	if creds != nil {
		opts = append(opts, grpc.Creds(creds))
	}

	// Bind the composition-root-supplied registrar (Option 3 #1152) to the
	// constructed server. It is also the instance read by the cell-attribution
	// interceptor, so attribution resolves through one shared map. validate()
	// guarantees cfg.Interceptors.Registrar() != nil.
	inner := grpc.NewServer(opts...)
	cfg.Interceptors.Registrar().BindServer(inner)
	return &Server{
		cfg:        cfg,
		grpcServer: inner,
		registrar:  cfg.Interceptors.Registrar(),
		serveDone:  make(chan struct{}),
	}, nil
}

// Registrar returns the cell-facing service registrar that bootstrap uses to
// register gRPC services (GAP-1 PR-7 [#1150]). It wraps the underlying
// *grpc.Server and intercepts RegisterService calls to record method→cellID
// attribution for each spec.
//
// The return type is cell.GRPCServiceRegistrar (the narrow kernel-defined
// interface) so bootstrap's GRPCServer interface can reference it without
// importing adapters/grpc or google.golang.org/grpc. The concrete value is
// *runtime/grpc.ServiceRegistrar, which callers that import adapters/grpc may
// type-assert to access CellIDForMethod (PR-9).
//
// All service registrations must happen before Serve/Worker().Start() to avoid
// data races — the bootstrap drain (phase7b) guarantees this ordering.
func (s *Server) Registrar() cell.GRPCServiceRegistrar {
	return s.registrar
}

// Probes implements lifecycle.ManagedResource. It returns a single probe named
// ProbeReady ("grpc_ready") that reports healthy (nil) when the server is
// actively serving and unhealthy when stopped or not yet started.
//
// The serving flag is set immediately after grpcServer.Serve starts (before the
// first Accept). A sub-millisecond window exists between the flag flip and the
// server being ready to accept connections; at typical readiness-probe polling
// intervals this window is invisible in practice.
func (s *Server) Probes() []healthz.Probe {
	return []healthz.Probe{
		healthz.NewProbe(ProbeReady, func(_ context.Context) error {
			if s.serving.Load() {
				return nil
			}
			return errcode.New(errcode.KindInternal, ErrAdapterGRPCServe,
				"grpc: server is not serving")
		}),
	}
}

// Worker implements lifecycle.ManagedResource. The returned worker's Start
// method blocks (binding the configured address and serving RPCs) until the
// context is canceled or GracefulStop completes. Stop initiates a graceful
// drain bounded by cfg.ShutdownTimeout.
func (s *Server) Worker() worker.Worker {
	return &serverWorker{s: s}
}

// Serve serves RPCs on the caller-supplied, already-bound listener until ctx is
// canceled or the server stops. It is the listener-injection counterpart of
// Worker().Start (which binds cfg.Addr internally): the composition root
// (runtime/bootstrap) pre-binds the socket synchronously — so port conflicts
// surface before any goroutine starts — and serves it here, exactly as the HTTP
// path calls http.Server.Serve(ln). Tests inject a bufconn listener the same
// way. Drain is driven by Close (graceful, bounded by cfg.ShutdownTimeout);
// ctx cancellation triggers the same drain as a fallback.
func (s *Server) Serve(ctx context.Context, lis net.Listener) error {
	return s.serve(ctx, lis)
}

// Close implements lifecycle.ManagedResource. It is equivalent to Worker().Stop()
// but may be called independently for LIFO teardown. Idempotent — safe to call
// multiple times or concurrently with Worker().Stop(). The second concurrent
// caller always returns nil even if the first caller's ctx expired (hard-stop
// path); this matches the LIFO bootstrap teardown contract.
func (s *Server) Close(ctx context.Context) error {
	return s.gracefulStop(ctx)
}

// serveAddr binds the configured address and delegates to serve.
func (s *Server) serveAddr(ctx context.Context) error {
	lis, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		// Log at Error: infrastructure failure prevents the server from accepting
		// any connections. Addr is kept out of the wire message (InternalAttr) to
		// avoid leaking internal network topology; it is visible in the slog output.
		slog.Error("grpc: net.Listen failed",
			slog.String("addr", s.cfg.Addr),
			slog.Any("error", err))
		return errcode.Wrap(errcode.KindInternal, ErrAdapterGRPCListen,
			"grpc: net.Listen failed", err,
			errcode.WithInternal(errcode.InternalAttr("addr", s.cfg.Addr)))
	}
	// Log the ACTUAL bound address, not cfg.Addr: cfg.Addr may be ":0"
	// (ephemeral port) and never reflects the resolved port. Mirrors the
	// bootstrap boundGRPC.boundAddr() funnel and the serve() error path below.
	slog.Info("grpc: server listening", slog.String("addr", lis.Addr().String()))
	return s.serve(ctx, lis)
}

// serve starts grpcServer.Serve on lis in a goroutine and blocks until either
// Serve returns (normal or error) or ctx is canceled. On ctx cancellation a
// graceful drain is attempted within cfg.ShutdownTimeout.
func (s *Server) serve(ctx context.Context, lis net.Listener) error {
	// addr is the ACTUAL served address (lis.Addr()), used for every log/error
	// in this function. It is correct for both serve paths: the listener-injection
	// path (Serve, where cfg.Addr may not match the injected socket) and the
	// self-bind path (serveAddr, where cfg.Addr may be ":0").
	addr := lis.Addr().String()
	warnIfInsecureNonLoopback(s.cfg.TLS.AllowInsecure, lis.Addr())

	// srvLog carries the resolved bound addr on every serve-lifecycle log line
	// (started / serve-returned / force-stopped) so the field never drifts as new
	// lifecycle logs are added here (#1791). gracefulStop's drain log lives in a
	// separate method where addr is out of scope and is server-wide, not
	// addr-specific, so it stays on the package logger.
	srvLog := slog.Default().With(slog.String("addr", addr))

	go func() {
		err := s.grpcServer.Serve(lis)
		s.serveErr = err
		close(s.serveDone)
	}()

	s.serving.Store(true)
	srvLog.Info("grpc: server started serving")

	select {
	case <-s.serveDone:
		s.serving.Store(false)
		err := s.serveErr
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		if err != nil {
			// Serve exited abnormally (not via Stop/GracefulStop). Log at Error
			// with structured context, mirroring the net.Listen failure path, so
			// operators see the cause even when the returned errcode is unwrapped
			// upstream. The raw addr stays in InternalAttr (server-side only).
			srvLog.Error("grpc: Serve returned unexpectedly",
				slog.Any("error", err))
			return errcode.Wrap(errcode.KindInternal, ErrAdapterGRPCServe,
				"grpc: Serve returned unexpectedly", err,
				errcode.WithInternal(errcode.InternalAttr("addr", addr)))
		}
		return nil

	case <-ctx.Done():
		// Detach from the canceled ctx's deadline but keep its values (trace,
		// request IDs): the drain budget is owned by cfg.ShutdownTimeout, not by
		// the already-canceled parent. Mirrors the runtime/websocket Hub
		// external-cancel shutdown pattern.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownTimeout)
		defer cancel()
		if err := s.gracefulStop(shutdownCtx); err != nil {
			srvLog.Warn("grpc: graceful stop exceeded shutdown budget; server force-stopped",
				slog.Any("error", err))
		}
		return ctx.Err()
	}
}

// warnIfInsecureNonLoopback logs a startup Warn when the server serves plaintext
// (AllowInsecure) on a non-loopback address. Plaintext on a non-loopback bind
// (e.g. 0.0.0.0) is a legitimate mesh-sidecar posture (see TLSConfig.AllowInsecure),
// so this is observability — not a fail-closed gate — mirroring the HTTP listener
// OPS-07 warning (runtime/bootstrap/bootstrap_phase7.go). It lives adapter-side
// because only the adapter knows its own TLSConfig and the resolved listener
// address (runtime/bootstrap holds no adapters/grpc dependency). Non-TCP
// listeners (e.g. bufconn in tests) and loopback binds are silent; wildcard_bind
// flags a 0.0.0.0 / :: bind (the highest-exposure case).
func warnIfInsecureNonLoopback(allowInsecure bool, addr net.Addr) {
	if !allowInsecure {
		return
	}
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok || tcpAddr.IP.IsLoopback() {
		return
	}
	slog.Warn("grpc: serving plaintext on a non-loopback address without TLS; "+
		"ensure network-level isolation (e.g. a service-mesh sidecar terminating TLS)",
		slog.String("addr", addr.String()),
		slog.Bool("wildcard_bind", tcpAddr.IP.IsUnspecified()))
}

// gracefulStop initiates a graceful drain of in-flight RPCs bounded by ctx.
// The body runs exactly once via stopOnce. If ctx expires before GracefulStop
// completes, grpcServer.Stop() is called for an immediate hard stop.
//
// Concurrency note: concurrent callers (Worker.Stop and Close on second call)
// block on stopOnce until the first caller's body exits, then both return nil.
// This is the documented idempotency: the second caller always returns nil even
// when the first caller's ctx expired (hard-stop path).
func (s *Server) gracefulStop(ctx context.Context) error {
	var stopErr error
	s.stopOnce.Do(func() {
		// Trigger the framework drain signal FIRST (PR-10 #1153): this cancels
		// every in-flight stream's handler context via the StreamDrain
		// interceptor, so a long-lived server-stream that selects on ctx.Done()
		// returns promptly and GracefulStop below completes within the budget
		// instead of waiting the full ShutdownTimeout for the hard Stop(). Drain
		// is required (validate guarantees non-nil); the trigger is idempotent and
		// a no-op for a server with no StreamDrain consumer.
		//
		// Log the drain start so the shutdown sequence has an ops anchor. The
		// resulting in-flight streams end with codes.Canceled — that metric spike
		// during a graceful stop is expected (see StreamMetrics godoc), not an
		// outage.
		slog.Info("grpc: draining — canceling in-flight streams before GracefulStop",
			slog.Duration("shutdown_timeout", s.cfg.ShutdownTimeout))
		s.cfg.Interceptors.Drain().Trigger()

		// Flip readiness to unhealthy BEFORE draining: GracefulStop stops
		// accepting new RPCs immediately, so the grpc_ready probe must report
		// not-serving the moment shutdown begins (lets a load balancer drain
		// this instance promptly instead of routing to a server that rejects
		// new streams). Drain of in-flight RPCs then proceeds below.
		s.serving.Store(false)

		graceDone := make(chan struct{})
		go func() {
			s.grpcServer.GracefulStop()
			close(graceDone)
		}()

		select {
		case <-graceDone:
			// Clean drain completed — goroutine has already exited. If Serve had
			// already returned independently (Serve error or a prior stop),
			// GracefulStop returns promptly on the already-stopping server, so
			// this case still fires without delay. We deliberately do NOT select
			// on serveDone: serveDone closes early in GracefulStop (listeners
			// shut before pending RPCs drain), so a serveDone case would let a
			// stuck RPC block <-graceDone while bypassing the ctx budget below.
		case <-ctx.Done():
			// Budget exceeded; hard stop unblocks GracefulStop.
			s.grpcServer.Stop()
			// Wait for the GracefulStop goroutine to finish before returning
			// so no goroutine outlives this call.
			<-graceDone
			stopErr = ctx.Err()
		}
	})
	return stopErr
}

// buildCredentials returns the transport credentials for the given TLSConfig.
//
// Three modes:
//   - AllowInsecure == true  →  plaintext (no grpc.Creds option added); returns (nil, nil)
//   - ClientCAPEM non-empty  →  mTLS via NewClientCAPool + NewServerMTLSConfig
//   - otherwise              →  server-side TLS via NewServerTLSConfig
//
// The nil-credentials return for AllowInsecure is intentional: grpc.NewServer
// without grpc.Creds listens in plaintext. This is an explicit-opt-in mode (dev
// or mesh-sidecar) guarded by Config.validate() (V5 fail-closed).
func buildCredentials(t TLSConfig) (credentials.TransportCredentials, error) {
	if t.AllowInsecure {
		// Plaintext: no transport credentials. Declare as typed nil so callers
		// can check `creds != nil` before appending grpc.Creds(creds).
		var creds credentials.TransportCredentials
		return creds, nil
	}

	if len(t.ClientCAPEM) > 0 {
		pool, err := tlsutil.NewClientCAPool(t.ClientCAPEM)
		if err != nil {
			// Caller-supplied bad PEM — KindInvalid (HTTP 400 semantic).
			return nil, errcode.Wrap(errcode.KindInvalid, ErrAdapterGRPCTLSConfig,
				"grpc: failed to build client CA pool for mTLS", err)
		}
		cfg, err := tlsutil.NewServerMTLSConfig(t.CertPEM, t.KeyPEM, pool)
		if err != nil {
			// Caller-supplied bad cert/key — KindInvalid.
			return nil, errcode.Wrap(errcode.KindInvalid, ErrAdapterGRPCTLSConfig,
				"grpc: failed to build mTLS server config", err)
		}
		return credentials.NewTLS(cfg), nil
	}

	cfg, err := tlsutil.NewServerTLSConfig(t.CertPEM, t.KeyPEM)
	if err != nil {
		// Caller-supplied bad cert/key PEM — KindInvalid.
		return nil, errcode.Wrap(errcode.KindInvalid, ErrAdapterGRPCTLSConfig,
			"grpc: failed to build TLS server config", err)
	}
	return credentials.NewTLS(cfg), nil
}

// serverWorker adapts *Server to the kernel/worker.Worker contract so that
// bootstrap.WithManagedResource auto-starts via WorkerGroup.
type serverWorker struct{ s *Server }

// Start blocks by binding the configured address and serving RPCs until ctx is
// canceled or the server stops. Implements worker.Worker.
func (w *serverWorker) Start(ctx context.Context) error {
	return w.s.serveAddr(ctx)
}

// Stop initiates graceful drain bounded by ctx. Implements worker.Worker.
// Idempotent — safe to call concurrently with Close.
func (w *serverWorker) Stop(ctx context.Context) error {
	return w.s.gracefulStop(ctx)
}
