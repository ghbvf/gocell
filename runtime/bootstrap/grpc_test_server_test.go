package bootstrap

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
	"github.com/ghbvf/gocell/runtime/grpc/interceptor"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
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
	reg := runtimegrpc.NewServiceRegistrar()
	drain := runtimegrpc.NewDrainSignal()
	deps := interceptor.Deps{
		Collector:   metrics.NewInMemoryGRPCCollector(),
		Clock:       clock.Real(),
		Verifier:    &bootstrapTestVerifier{},
		AuthOptions: authOpts,
		Registrar:   reg,
		CellIDClosedSet: []string{
			"bootstrap-test-cell",
			"_listener-test",
		},
		Drain: drain,
	}
	bundle := interceptor.NewServerInterceptors(deps)
	srv := grpc.NewServer(bundle.ServerOptions()...)
	reg.BindServer(srv)
	return &testGRPCServer{inner: srv, registrar: reg, drain: drain}
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
