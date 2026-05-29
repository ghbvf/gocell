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

	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
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
// Construction: call New(cfg) to obtain a configured *Server, then register
// services via ServiceRegistrar() before starting the Worker.
//
// Concurrency: all public methods are safe for concurrent use. GracefulStop
// is idempotent via stopOnce — calling Worker().Stop() and Close() simultaneously
// (LIFO teardown) is safe.
type Server struct {
	cfg        Config
	grpcServer *grpc.Server

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

	var opts []grpc.ServerOption
	if creds != nil {
		opts = append(opts, grpc.Creds(creds))
	}

	return &Server{
		cfg:        cfg,
		grpcServer: grpc.NewServer(opts...),
		serveDone:  make(chan struct{}),
	}, nil
}

// ServiceRegistrar returns the grpc.ServiceRegistrar so callers can register
// gRPC service implementations before the Worker starts. Must be called before
// Worker().Start() to avoid data races on the service registry.
func (s *Server) ServiceRegistrar() grpc.ServiceRegistrar {
	return s.grpcServer
}

// Probes implements lifecycle.ManagedResource. It returns a single probe named
// ProbeReady ("grpc_ready") that reports healthy (nil) when the server is
// actively serving and unhealthy when stopped or not yet started.
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

// Close implements lifecycle.ManagedResource. It is equivalent to Worker().Stop()
// but may be called independently for LIFO teardown. Idempotent — safe to call
// multiple times or concurrently with Worker().Stop().
func (s *Server) Close(ctx context.Context) error {
	return s.gracefulStop(ctx)
}

// serveAddr binds the configured address and delegates to serve.
func (s *Server) serveAddr(ctx context.Context) error {
	lis, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterGRPCListen,
			"grpc: net.Listen failed", err,
			errcode.WithInternal(errcode.InternalAttr("addr", s.cfg.Addr)))
	}
	slog.Info("grpc: server listening", slog.String("addr", s.cfg.Addr))
	return s.serve(ctx, lis)
}

// serve starts grpcServer.Serve on lis in a goroutine and blocks until either
// Serve returns (normal or error) or ctx is canceled. On ctx cancellation a
// graceful drain is attempted within cfg.ShutdownTimeout.
func (s *Server) serve(ctx context.Context, lis net.Listener) error {
	go func() {
		err := s.grpcServer.Serve(lis)
		s.serveErr = err
		close(s.serveDone)
	}()

	s.serving.Store(true)
	slog.Info("grpc: server started serving")

	select {
	case <-s.serveDone:
		s.serving.Store(false)
		err := s.serveErr
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		if err != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterGRPCServe,
				"grpc: Serve returned unexpectedly", err)
		}
		return nil

	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		if err := s.gracefulStop(shutdownCtx); err != nil {
			slog.Warn("grpc: graceful stop exceeded shutdown budget; server force-stopped",
				slog.Any("error", err))
		}
		return ctx.Err()
	}
}

// gracefulStop initiates a graceful drain of in-flight RPCs bounded by ctx.
// The body runs exactly once via stopOnce. If ctx expires before GracefulStop
// completes, grpcServer.Stop() is called for an immediate hard stop.
func (s *Server) gracefulStop(ctx context.Context) error {
	var stopErr error
	s.stopOnce.Do(func() {
		graceDone := make(chan struct{})
		go func() {
			s.grpcServer.GracefulStop()
			close(graceDone)
		}()

		select {
		case <-graceDone:
			// Clean drain completed.
		case <-s.serveDone:
			// Serve already returned — nothing left to drain.
		case <-ctx.Done():
			// Budget exceeded; hard stop.
			s.grpcServer.Stop()
			stopErr = ctx.Err()
		}
		s.serving.Store(false)
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
// without grpc.Creds listens in plaintext. This is a dev-only mode guarded by
// Config.validate() (V5 fail-closed).
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
			return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterGRPCTLSConfig,
				"grpc: failed to build client CA pool for mTLS", err)
		}
		cfg, err := tlsutil.NewServerMTLSConfig(t.CertPEM, t.KeyPEM, pool)
		if err != nil {
			return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterGRPCTLSConfig,
				"grpc: failed to build mTLS server config", err)
		}
		return credentials.NewTLS(cfg), nil
	}

	cfg, err := tlsutil.NewServerTLSConfig(t.CertPEM, t.KeyPEM)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterGRPCTLSConfig,
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
