package bootstrap

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	runtimegrpc "github.com/ghbvf/gocell/framework/runtime/grpc"
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
	"github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

const grpcTestReadyProbe healthz.ProbeName = "grpc_ready"

type testGRPCServer struct {
	inner     *grpc.Server
	registrar *runtimegrpc.ServiceRegistrar
	drain     *runtimegrpc.DrainSignal

	serving atomic.Bool
	stopMu  sync.Mutex
	stopped bool
}

var _ GRPCServer = (*testGRPCServer)(nil)

func newTestGRPCServer(authPublic func(fullMethod string) bool) *testGRPCServer {
	var authOpts []interceptor.AuthOption
	if authPublic != nil {
		authOpts = append(authOpts, interceptor.WithPublicMethod(authPublic))
	}
	deps := interceptor.Deps{
		Collector:   metrics.NewInMemoryGRPCCollector(),
		Clock:       clock.Real(),
		Verifier:    &bootstrapTestVerifier{},
		AuthOptions: authOpts,
		CellIDClosedSet: []string{
			"bootstrap-test-cell",
			"_listener-test",
		},
	}
	// NewServerInterceptors mints the shared registrar/drain (#1752); take them
	// from the bundle so this server binds exactly the instances the chains read.
	bundle := interceptor.NewServerInterceptors(deps)
	srv := grpc.NewServer(bundle.ServerOptions()...)
	reg := bundle.Registrar()
	reg.BindServer(srv)
	return &testGRPCServer{inner: srv, registrar: reg, drain: bundle.Drain()}
}

func (s *testGRPCServer) Serve(ctx context.Context, lis net.Listener) error {
	errCh := make(chan error, 1)
	go func() {
		s.serving.Store(true)
		errCh <- s.inner.Serve(lis)
	}()

	select {
	case err := <-errCh:
		s.serving.Store(false)
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), testtime.D2s)
		defer cancel()
		_ = s.Close(shutdownCtx)
		err := <-errCh
		s.serving.Store(false)
		if errors.Is(err, grpc.ErrServerStopped) {
			return ctx.Err()
		}
		return err
	}
}

func (s *testGRPCServer) Close(ctx context.Context) error {
	s.stopMu.Lock()
	if s.stopped {
		s.stopMu.Unlock()
		return nil
	}
	s.stopped = true
	s.stopMu.Unlock()

	s.drain.Trigger()
	s.serving.Store(false)

	done := make(chan struct{})
	go func() {
		s.inner.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.inner.Stop()
		<-done
		return ctx.Err()
	}
}

func (s *testGRPCServer) Registrar() GRPCServiceRegistrar {
	return s.registrar
}

func (s *testGRPCServer) Probes() []healthz.Probe {
	return []healthz.Probe{
		healthz.NewProbe(grpcTestReadyProbe, func(context.Context) error {
			if s.serving.Load() {
				return nil
			}
			return errcode.New(errcode.KindInternal, errcode.ErrInternal, "grpc: server is not serving")
		}),
	}
}

func registerTestGRPCService(srv *testGRPCServer, register func(grpc.ServiceRegistrar)) error {
	if register == nil {
		return nil
	}
	return srv.Registrar().Register(cell.GRPCServiceSpec{
		ContractID: "grpc.listener.test.v1",
		CellID:     "_listener-test",
		Listener:   cell.PrimaryListener,
		Register:   register,
	})
}
