//go:build integration

package grpc_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	grpcadapter "github.com/ghbvf/gocell/adapters/grpc"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/runtime/auth"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
	"github.com/ghbvf/gocell/runtime/grpc/interceptor"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

const (
	// integServeTimeout bounds the serve goroutine in each integration test.
	integServeTimeout = 5 * time.Second
	// integDialTimeout bounds the client dial attempt.
	integDialTimeout = 2 * time.Second
	// integProbePollTick is the poll interval for testwait.External probe checks.
	integProbePollTick = testtime.D5ms
)

// startServing starts the server on lis in a goroutine and returns a stop function.
// The serve goroutine exits when stopFn is called or after integServeTimeout.
func startServing(t *testing.T, srv *grpcadapter.Server, lis net.Listener) (stopFn func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integServeTimeout)
	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx, lis)
	}()
	return func() {
		cancel()
		<-done
	}
}

// dialInsecure dials the given address with insecure (plaintext) credentials.
func dialInsecure(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	// Force a connect attempt; actual readiness is verified by the caller via
	// healthCheck or waitForServing — grpc.NewClient does not accept a ctx.
	cc.Connect()
	return cc
}

// dialTLS dials the given address with server-side TLS credentials (no client cert).
func dialTLS(t *testing.T, addr string, rootCAs *x509.CertPool, serverName string) *grpc.ClientConn {
	t.Helper()
	tlsCfg := &tls.Config{
		RootCAs:    rootCAs,
		ServerName: serverName,
		MinVersion: tls.VersionTLS13,
	}
	cc, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	require.NoError(t, err)
	return cc
}

// dialMTLS dials with mTLS (server validates client cert; client validates server cert).
func dialMTLS(t *testing.T, addr string, rootCAs *x509.CertPool, clientCert tls.Certificate, serverName string) *grpc.ClientConn {
	t.Helper()
	tlsCfg := &tls.Config{
		RootCAs:      rootCAs,
		Certificates: []tls.Certificate{clientCert},
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS13,
	}
	cc, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	require.NoError(t, err)
	return cc
}

// blockerMethod is the full method name of the test-only blocking unary RPC
// registered by blockingServiceDesc. It reuses grpc_health_v1 proto messages as
// the wire payload so no extra codegen/proto dependency is needed.
const blockerMethod = "/grpctest.Blocker/Block"

// blockingServiceDesc returns a ServiceDesc with one unary method that closes
// started when the handler is entered, then blocks until release is closed. This
// lets a test hold an RPC in-flight across a graceful drain to verify the server
// waits for it (rather than hard-killing it).
func blockingServiceDesc(started chan<- struct{}, release <-chan struct{}) grpc.ServiceDesc {
	return grpc.ServiceDesc{
		ServiceName: "grpctest.Blocker",
		HandlerType: (*any)(nil), // any: any impl satisfies it; avoids a typed stub
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Block",
				Handler: func(_ any, _ context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
					req := new(grpc_health_v1.HealthCheckRequest)
					if err := dec(req); err != nil {
						return nil, err
					}
					close(started)
					<-release
					return &grpc_health_v1.HealthCheckResponse{
						Status: grpc_health_v1.HealthCheckResponse_SERVING,
					}, nil
				},
			},
		},
	}
}

// healthCheck performs a unary grpc_health_v1.Health.Check RPC and returns the response status.
func healthCheck(t *testing.T, cc *grpc.ClientConn) (grpc_health_v1.HealthCheckResponse_ServingStatus, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integDialTimeout)
	defer cancel()
	client := grpc_health_v1.NewHealthClient(cc)
	resp, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		return 0, err
	}
	return resp.GetStatus(), nil
}

// integRegisterHealth registers the gRPC health service on srv via the Form B
// callback path (srv.Registrar().Register(spec)).
func integRegisterHealth(t *testing.T, srv *grpcadapter.Server, healthSrv *health.Server) {
	t.Helper()
	spec := cell.GRPCServiceSpec{
		ContractID: "grpc.health.v1",
		CellID:     "_integration-test",
		Listener:   cell.PrimaryListener,
		Register: func(r grpc.ServiceRegistrar) {
			grpc_health_v1.RegisterHealthServer(r, healthSrv)
		},
	}
	require.NoError(t, srv.Registrar().Register(spec))
}

// integRegisterDesc registers an arbitrary ServiceDesc on srv via the Form B
// callback path (srv.Registrar().Register(spec)).
func integRegisterDesc(t *testing.T, srv *grpcadapter.Server, contractID string, desc *grpc.ServiceDesc, impl any) {
	t.Helper()
	spec := cell.GRPCServiceSpec{
		ContractID: contractID,
		CellID:     "_integration-test",
		Listener:   cell.PrimaryListener,
		Register: func(r grpc.ServiceRegistrar) {
			r.RegisterService(desc, impl)
		},
	}
	require.NoError(t, srv.Registrar().Register(spec))
}

// ─── Integration tests ────────────────────────────────────────────────────────

// TestIntegration_Plaintext_BufconnCheck verifies plaintext gRPC via bufconn.
func TestIntegration_Plaintext_BufconnCheck(t *testing.T) {
	t.Parallel()

	cfg := grpcadapter.Config{
		Addr:            ":0",
		ShutdownTimeout: integServeTimeout,
		TLS:             grpcadapter.TLSConfig{AllowInsecure: true},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)

	// Register health service so the RPC call has something to hit.
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	integRegisterHealth(t, srv, healthSrv)

	// Use bufconn for in-process networking.
	bufSize := 1024 * 1024
	lis := newBufconnListener(bufSize)

	stop := startServing(t, srv, lis)
	defer stop()

	// Give the server a moment to start serving so the probe returns healthy.
	waitForServing(t, srv)

	// Verify probe is healthy.
	probes := srv.Probes()
	require.Len(t, probes, 1)
	require.NoError(t, probes[0].Check(context.Background()), "probe must be healthy after serving starts")

	// Dial via bufconn.
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
	)
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	st, err := healthCheck(t, cc)
	require.NoError(t, err)
	assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, st)
}

// TestIntegration_ServerTLS verifies single-direction TLS via real loopback.
func TestIntegration_ServerTLS(t *testing.T) {
	t.Parallel()

	chain := genIntegChain(t)

	cfg := grpcadapter.Config{
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: integServeTimeout,
		TLS: grpcadapter.TLSConfig{
			CertPEM: chain.serverCertPEM,
			KeyPEM:  chain.serverKeyPEM,
		},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	integRegisterHealth(t, srv, healthSrv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()

	stop := startServing(t, srv, lis)
	defer stop()

	waitForServing(t, srv)

	// Build client trust pool from the CA that signed the server cert.
	rootCAs := x509.NewCertPool()
	require.True(t, rootCAs.AppendCertsFromPEM(chain.rootCertPEM))

	cc := dialTLS(t, addr, rootCAs, "localhost")
	defer func() { _ = cc.Close() }()

	st, err := healthCheck(t, cc)
	require.NoError(t, err)
	assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, st)
}

// TestIntegration_MTLS_WithValidClientCert verifies mTLS with a CA-signed client cert.
func TestIntegration_MTLS_WithValidClientCert(t *testing.T) {
	t.Parallel()

	chain := genIntegChain(t)

	cfg := grpcadapter.Config{
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: integServeTimeout,
		TLS: grpcadapter.TLSConfig{
			CertPEM:     chain.serverCertPEM,
			KeyPEM:      chain.serverKeyPEM,
			ClientCAPEM: chain.rootCertPEM,
		},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	integRegisterHealth(t, srv, healthSrv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()

	stop := startServing(t, srv, lis)
	defer stop()

	waitForServing(t, srv)

	rootCAs := x509.NewCertPool()
	require.True(t, rootCAs.AppendCertsFromPEM(chain.rootCertPEM))

	cc := dialMTLS(t, addr, rootCAs, chain.clientCert, "localhost")
	defer func() { _ = cc.Close() }()

	st, err := healthCheck(t, cc)
	require.NoError(t, err)
	assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, st)
}

// TestIntegration_MTLS_NoClientCert verifies that mTLS rejects a client without a cert.
// This is the critical security negative test.
func TestIntegration_MTLS_NoClientCert(t *testing.T) {
	t.Parallel()

	chain := genIntegChain(t)

	cfg := grpcadapter.Config{
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: integServeTimeout,
		TLS: grpcadapter.TLSConfig{
			CertPEM:     chain.serverCertPEM,
			KeyPEM:      chain.serverKeyPEM,
			ClientCAPEM: chain.rootCertPEM,
		},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	integRegisterHealth(t, srv, healthSrv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()

	stop := startServing(t, srv, lis)
	defer stop()

	waitForServing(t, srv)

	// Client with no client certificate — must be rejected by the server.
	rootCAs := x509.NewCertPool()
	require.True(t, rootCAs.AppendCertsFromPEM(chain.rootCertPEM))

	cc := dialTLS(t, addr, rootCAs, "localhost") // no client cert
	defer func() { _ = cc.Close() }()

	_, err = healthCheck(t, cc)
	require.Error(t, err, "mTLS server must reject client without certificate")

	// The security-meaningful invariant is that the RPC is REJECTED at the
	// transport handshake (server enforces RequireAndVerifyClientCert), which
	// grpc-go surfaces as codes.Unavailable — the rejection happens before any
	// RPC frame is exchanged, so it is not Unauthenticated. We assert only the
	// code: the exact error message text varies by TLS-alert timing across
	// platforms/Go versions ("tls: certificate required" vs "remote error" vs
	// "EOF"), so a substring check on the message is flaky and asserts nothing
	// about the security property.
	code := status.Code(err)
	assert.Equal(t, codes.Unavailable, code,
		"expected Unavailable from mTLS handshake rejection, got %v", code)
}

// TestIntegration_GracefulDrain verifies that an in-flight RPC actually held
// across the drain completes before Close returns, AND that readiness flips to
// unhealthy the moment the drain begins (F2). This exercises the real graceful
// path (a blocking handler spanning shutdown), not just Close on an idle server.
func TestIntegration_GracefulDrain(t *testing.T) {
	t.Parallel()

	cfg := grpcadapter.Config{
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: integServeTimeout,
		TLS:             grpcadapter.TLSConfig{AllowInsecure: true},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)

	rpcStarted := make(chan struct{})
	release := make(chan struct{})
	desc := blockingServiceDesc(rpcStarted, release)
	integRegisterDesc(t, srv, "grpc.test.blocker.v1", &desc, new(any))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()

	serveCtx, serveCancel := context.WithTimeout(context.Background(), integServeTimeout)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- srv.Serve(serveCtx, lis)
	}()
	defer serveCancel()

	waitForServing(t, srv)

	cc := dialInsecure(t, addr)
	defer func() { _ = cc.Close() }()

	// Fire the blocking RPC and wait until the handler is actually executing,
	// so it is genuinely in-flight when the drain starts.
	rpcErr := make(chan error, 1)
	rpcResp := make(chan *grpc_health_v1.HealthCheckResponse, 1)
	go func() {
		resp := new(grpc_health_v1.HealthCheckResponse)
		err := cc.Invoke(context.Background(), blockerMethod,
			&grpc_health_v1.HealthCheckRequest{}, resp)
		if err != nil {
			rpcErr <- err
			return
		}
		rpcResp <- resp
	}()
	testwait.Deterministic(t, rpcStarted, "blocking-rpc-started")

	// Begin graceful stop in the background; it must block until the in-flight
	// RPC is released.
	closeReturned := make(chan error, 1)
	go func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), integServeTimeout)
		defer closeCancel()
		closeReturned <- srv.Close(closeCtx)
	}()

	// F2: readiness must flip to unhealthy as soon as the drain begins, even
	// though the in-flight RPC has NOT completed yet.
	probes := srv.Probes()
	require.Len(t, probes, 1)
	testwait.External(t, "grpc-ready-unhealthy-during-drain", func() bool {
		return probes[0].Check(context.Background()) != nil
	}, integDialTimeout, integProbePollTick)

	// Close must NOT have returned yet — the RPC is still blocked.
	select {
	case <-closeReturned:
		t.Fatal("Close returned before the in-flight RPC was released; drain did not wait")
	default:
	}

	// Release the RPC; both it and Close should now complete cleanly.
	close(release)
	closeErr := testwait.Deterministic(t, closeReturned, "close-returns-after-drain")
	assert.NoError(t, closeErr, "Close should succeed within shutdown budget")
	select {
	case err := <-rpcErr:
		t.Fatalf("in-flight RPC failed instead of draining cleanly: %v", err)
	case resp := <-rpcResp:
		assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, resp.GetStatus())
	}
}

// TestIntegration_WorkerStopAndCloseConcurrent verifies that calling Worker().Stop()
// and Close() concurrently is safe: both must return without panic or deadlock,
// and the probe must report unhealthy afterwards (the declared stopOnce invariant).
func TestIntegration_WorkerStopAndCloseConcurrent(t *testing.T) {
	t.Parallel()

	cfg := grpcadapter.Config{
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: integServeTimeout,
		TLS:             grpcadapter.TLSConfig{AllowInsecure: true},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	serveCtx, serveCancel := context.WithTimeout(context.Background(), integServeTimeout)
	defer serveCancel()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- srv.Serve(serveCtx, lis)
	}()

	waitForServing(t, srv)

	// Fire Worker().Stop() and Close() simultaneously from two goroutines.
	teardownCtx, teardownCancel := context.WithTimeout(context.Background(), integServeTimeout)
	defer teardownCancel()

	stopDone := make(chan error, 1)
	closeDone := make(chan error, 1)
	go func() { stopDone <- srv.Worker().Stop(teardownCtx) }()
	go func() { closeDone <- srv.Close(teardownCtx) }()

	stopErr := testwait.Deterministic(t, stopDone, "worker-stop")
	closeErr := testwait.Deterministic(t, closeDone, "close")

	// Both callers must not return a non-nil error in the happy path.
	// The second caller always returns nil (stopOnce semantic).
	assert.NoError(t, stopErr, "Worker().Stop() must not error on concurrent teardown")
	assert.NoError(t, closeErr, "Close() must not error on concurrent teardown")

	// Probe must be unhealthy after teardown regardless of which call won the Once.
	probes := srv.Probes()
	require.Len(t, probes, 1)
	assert.Error(t, probes[0].Check(context.Background()),
		"probe must be unhealthy after concurrent Stop+Close")
}

// TestIntegration_ServeContextCancel_GracefulDrain verifies the serve() ctx.Done
// branch performs a real GRACEFUL drain — not a hard stop — when the context
// passed to Serve is canceled (simulating a WorkerGroup / bootstrap
// external cancel: sibling worker crash, SIGTERM, etc.).
//
// To prove gracefulness (rather than merely that the branch returns
// context.Canceled), the test holds a unary RPC in-flight across the cancel and
// asserts serve() does NOT return until that RPC is released, then that the RPC
// completes with its real response instead of being killed mid-flight. This also
// pins the WithoutCancel budget detachment: serve drains within its self-owned
// cfg.ShutdownTimeout (context.WithoutCancel(ctx)), so a regression that reused the
// already-canceled parent ctx as the drain budget — collapsing the graceful drain
// into an immediate hard Stop() — would return before release and fail this test.
//
// It mirrors the runtime/websocket Hub external-cancel shutdown pattern documented
// in server.go, and complements TestIntegration_GracefulDrain (which drives the
// same gracefulStop through Close()'s own ctx).
func TestIntegration_ServeContextCancel_GracefulDrain(t *testing.T) {
	t.Parallel()

	cfg := grpcadapter.Config{
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: integServeTimeout,
		TLS:             grpcadapter.TLSConfig{AllowInsecure: true},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)

	// A blocking unary RPC lets the test hold a request in-flight across the cancel
	// so it can prove the drain WAITS for it (graceful) rather than hard-killing it.
	rpcStarted := make(chan struct{})
	release := make(chan struct{})
	desc := blockingServiceDesc(rpcStarted, release)
	integRegisterDesc(t, srv, "grpc.test.blocker.v1", &desc, new(any))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()

	// cancelableCtx simulates the WorkerGroup / bootstrap parent ctx being
	// canceled externally (sibling worker crash, SIGTERM, etc.).
	cancelableCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- srv.Serve(cancelableCtx, lis)
	}()

	waitForServing(t, srv)

	// Fire the blocking RPC and wait until the handler is actually executing, so
	// it is genuinely in-flight when the external cancel triggers the drain. The
	// RPC uses its own context.Background(): canceling the serve ctx must not
	// cancel the in-flight RPC, only trigger the graceful drain around it.
	cc := dialInsecure(t, addr)
	defer func() { _ = cc.Close() }()
	rpcErr := make(chan error, 1)
	rpcResp := make(chan *grpc_health_v1.HealthCheckResponse, 1)
	go func() {
		resp := new(grpc_health_v1.HealthCheckResponse)
		if err := cc.Invoke(context.Background(), blockerMethod,
			&grpc_health_v1.HealthCheckRequest{}, resp); err != nil {
			rpcErr <- err
			return
		}
		rpcResp <- resp
	}()
	testwait.Deterministic(t, rpcStarted, "blocking-rpc-started")

	// Core trigger: cancel the serve ctx while the RPC is in-flight. This drives
	// serve()'s ctx.Done branch, which must begin a graceful drain bounded by the
	// self-owned ShutdownTimeout (NOT the already-canceled parent ctx).
	cancel()

	// Readiness must flip to unhealthy as soon as the drain begins, even though
	// the in-flight RPC has not completed yet.
	probes := srv.Probes()
	require.Len(t, probes, 1)
	testwait.External(t, "grpc-ready-unhealthy-during-ctxcancel-drain", func() bool {
		return probes[0].Check(context.Background()) != nil
	}, integDialTimeout, integProbePollTick)

	// serve() must NOT have returned yet: the graceful drain is waiting for the
	// in-flight RPC. A hard stop — or reusing the canceled parent ctx as the drain
	// budget — would return here before release; that is the regression guarded.
	select {
	case err := <-serveDone:
		t.Fatalf("serve returned before the in-flight RPC was released "+
			"(drain did not wait — hard stop or canceled-ctx budget): %v", err)
	default:
	}

	// Release the RPC; the drain now completes and serve returns.
	close(release)

	serveErr := testwait.Deterministic(t, serveDone, "serve-returns-after-drain")
	assert.ErrorIs(t, serveErr, context.Canceled,
		"serve must return context.Canceled after the ctx-cancel drain completes")

	// The in-flight RPC must have drained cleanly (served its real response), not
	// been hard-killed mid-flight.
	select {
	case err := <-rpcErr:
		t.Fatalf("in-flight RPC failed instead of draining cleanly: %v", err)
	case resp := <-rpcResp:
		assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, resp.GetStatus(),
			"in-flight RPC must complete with its real response after graceful drain")
	}

	// After serve returns, serving must be false.
	assert.Error(t, probes[0].Check(context.Background()),
		"probe must be unhealthy after serve returns from ctx-cancel drain")
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// waitForServing polls srv.Probes()[0].Check until it returns nil or times out.
// Uses testwait.External because the probe state transitions inside a goroutine
// managed by grpcServer.Serve — no channel signal is producible by the caller.
func waitForServing(t *testing.T, srv *grpcadapter.Server) {
	t.Helper()
	probes := srv.Probes()
	require.Len(t, probes, 1)
	probe := probes[0]
	testwait.External(t, "grpc-server-probe-healthy",
		func() bool { return probe.Check(context.Background()) == nil },
		testtime.EventuallyDefault, testtime.FastPoll,
		"server did not start serving within %v", testtime.EventuallyDefault,
	)
}

// newBufconnListener creates an in-process bufconn listener with the given
// buffer size. Used by plaintext integration tests to avoid OS-level networking.
func newBufconnListener(bufSize int) *bufconn.Listener {
	return bufconn.Listen(bufSize)
}

// dialBufconn dials the in-process bufconn listener over plaintext.
func dialBufconn(t *testing.T, lis *bufconn.Listener) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
	)
	require.NoError(t, err)
	return cc
}

// ─── Streaming integration (PR-10 #1153) ───────────────────────────────────────

// integVerifier is a stub IntentTokenVerifier. The streaming tests either mark
// the method public (auth bypassed, verifier never called) or send no token
// (StreamAuth rejects before the verifier runs), so it only needs to be non-nil.
type integVerifier struct{}

func (integVerifier) VerifyIntent(context.Context, string, auth.TokenIntent) (auth.Claims, error) {
	return auth.Claims{}, errors.New("integ verifier: no tokens issued in this test")
}

// newStreamingServer builds a plaintext bufconn-ready server whose adapter Config
// carries one interceptor deps object. grpcadapter.New must derive both unary and
// stream chains from it, sharing one Registrar and one DrainSignal between the
// chains and the adapter config (Option 3, #1152/#1153).
func newStreamingServer(t *testing.T, authOpts ...interceptor.AuthOption) (*grpcadapter.Server, *runtimegrpc.DrainSignal) {
	t.Helper()
	reg := runtimegrpc.NewServiceRegistrar()
	drain := runtimegrpc.NewDrainSignal()
	deps := interceptor.Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        integVerifier{},
		AuthOptions:     authOpts,
		Registrar:       reg,
		CellIDClosedSet: []string{"_integration-test"},
		Drain:           drain,
	}
	srv, err := grpcadapter.New(grpcadapter.Config{
		Addr:            ":0",
		ShutdownTimeout: integServeTimeout,
		TLS:             grpcadapter.TLSConfig{AllowInsecure: true},
		Interceptors:    deps,
	})
	require.NoError(t, err)
	return srv, drain
}

// allMethodsPublic marks every method public so the streaming round-trip tests
// exercise the full chain without minting tokens.
func allMethodsPublic() interceptor.AuthOption {
	return interceptor.WithPublicMethod(func(string) bool { return true })
}

const (
	streamerServerStream = "/grpctest.Streamer/ServerStream"
	streamerClientStream = "/grpctest.Streamer/ClientStream"
	streamerBidi         = "/grpctest.Streamer/Bidi"
	streamerBlock        = "/grpctest.Streamer/BlockStream"
)

func healthReq() *grpc_health_v1.HealthCheckRequest { return &grpc_health_v1.HealthCheckRequest{} }
func healthResp() *grpc_health_v1.HealthCheckResponse {
	return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}
}

// streamerServiceDesc registers server-stream, client-stream, bidi, and a
// drain-blocking method (reusing health proto messages as the wire payload, so no
// extra codegen). BlockStream sends one message, signals started, then blocks on
// ctx.Done() so a test can hold a stream in-flight across a graceful drain.
func streamerServiceDesc(started chan<- struct{}) grpc.ServiceDesc {
	return grpc.ServiceDesc{
		ServiceName: "grpctest.Streamer",
		HandlerType: (*any)(nil),
		Streams: []grpc.StreamDesc{
			{
				StreamName:    "ServerStream",
				ServerStreams: true,
				Handler: func(_ any, ss grpc.ServerStream) error {
					if err := ss.RecvMsg(healthReq()); err != nil {
						return err
					}
					for i := 0; i < 3; i++ {
						if err := ss.Context().Err(); err != nil {
							return err
						}
						if err := ss.SendMsg(healthResp()); err != nil {
							return err
						}
					}
					return nil
				},
			},
			{
				StreamName:    "ClientStream",
				ClientStreams: true,
				Handler: func(_ any, ss grpc.ServerStream) error {
					for {
						if err := ss.RecvMsg(healthReq()); err != nil {
							if errors.Is(err, io.EOF) {
								break
							}
							return err
						}
					}
					return ss.SendMsg(healthResp())
				},
			},
			{
				StreamName:    "Bidi",
				ServerStreams: true,
				ClientStreams: true,
				Handler: func(_ any, ss grpc.ServerStream) error {
					for {
						if err := ss.RecvMsg(healthReq()); err != nil {
							if errors.Is(err, io.EOF) {
								return nil
							}
							return err
						}
						if err := ss.SendMsg(healthResp()); err != nil {
							return err
						}
					}
				},
			},
			{
				StreamName:    "BlockStream",
				ServerStreams: true,
				Handler: func(_ any, ss grpc.ServerStream) error {
					if err := ss.RecvMsg(healthReq()); err != nil {
						return err
					}
					if err := ss.SendMsg(healthResp()); err != nil {
						return err
					}
					close(started)
					<-ss.Context().Done() // block until the drain cancels the stream ctx
					return ss.Context().Err()
				},
			},
		},
	}
}

// TestIntegration_Streaming_RoundTrip drives all three streaming modes through the
// full unary+stream interceptor chains over bufconn, proving the chain (including
// auth and the wrapped-stream ctx threading) does not break streaming.
func TestIntegration_Streaming_RoundTrip(t *testing.T) {
	t.Parallel()
	srv, _ := newStreamingServer(t, allMethodsPublic())
	desc := streamerServiceDesc(make(chan struct{}))
	integRegisterDesc(t, srv, "grpc.streamer.v1", &desc, new(any))

	lis := newBufconnListener(1024 * 1024)
	stop := startServing(t, srv, lis)
	defer stop()
	waitForServing(t, srv)

	cc := dialBufconn(t, lis)
	defer func() { _ = cc.Close() }()

	t.Run("server-stream", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), integDialTimeout)
		defer cancel()
		cs, err := cc.NewStream(ctx, &grpc.StreamDesc{StreamName: "ServerStream", ServerStreams: true}, streamerServerStream)
		require.NoError(t, err)
		require.NoError(t, cs.SendMsg(healthReq()))
		require.NoError(t, cs.CloseSend())
		got := 0
		for {
			if err := cs.RecvMsg(new(grpc_health_v1.HealthCheckResponse)); err != nil {
				require.ErrorIs(t, err, io.EOF)
				break
			}
			got++
		}
		require.Equal(t, 3, got, "server-stream must deliver all 3 messages")
	})

	t.Run("client-stream", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), integDialTimeout)
		defer cancel()
		cs, err := cc.NewStream(ctx, &grpc.StreamDesc{StreamName: "ClientStream", ClientStreams: true}, streamerClientStream)
		require.NoError(t, err)
		for i := 0; i < 3; i++ {
			require.NoError(t, cs.SendMsg(healthReq()))
		}
		require.NoError(t, cs.CloseSend())
		require.NoError(t, cs.RecvMsg(new(grpc_health_v1.HealthCheckResponse)), "client-stream must return one response")
	})

	t.Run("bidi", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), integDialTimeout)
		defer cancel()
		cs, err := cc.NewStream(ctx, &grpc.StreamDesc{StreamName: "Bidi", ServerStreams: true, ClientStreams: true}, streamerBidi)
		require.NoError(t, err)
		for i := 0; i < 3; i++ {
			require.NoError(t, cs.SendMsg(healthReq()))
			require.NoError(t, cs.RecvMsg(new(grpc_health_v1.HealthCheckResponse)))
		}
		require.NoError(t, cs.CloseSend())
		require.ErrorIs(t, cs.RecvMsg(new(grpc_health_v1.HealthCheckResponse)), io.EOF, "bidi must EOF after CloseSend drains")
	})
}

// TestIntegration_Streaming_AuthRejectsWithoutToken proves the stream chain
// authenticates streams: with no public-method bypass and no bearer metadata,
// StreamAuth rejects the stream with codes.Unauthenticated — shipping streaming
// without the stream auth interceptor would expose unauthenticated streams.
func TestIntegration_Streaming_AuthRejectsWithoutToken(t *testing.T) {
	t.Parallel()
	srv, _ := newStreamingServer(t) // no allMethodsPublic → fail-closed auth
	desc := streamerServiceDesc(make(chan struct{}))
	integRegisterDesc(t, srv, "grpc.streamer.v1", &desc, new(any))

	lis := newBufconnListener(1024 * 1024)
	stop := startServing(t, srv, lis)
	defer stop()
	waitForServing(t, srv)

	cc := dialBufconn(t, lis)
	defer func() { _ = cc.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), integDialTimeout)
	defer cancel()
	cs, err := cc.NewStream(ctx, &grpc.StreamDesc{StreamName: "ServerStream", ServerStreams: true}, streamerServerStream)
	require.NoError(t, err)
	_ = cs.SendMsg(healthReq())
	err = cs.RecvMsg(new(grpc_health_v1.HealthCheckResponse))
	require.Equal(t, codes.Unauthenticated, status.Code(err),
		"a stream without bearer metadata must be rejected Unauthenticated by StreamAuth")
}

// TestIntegration_Streaming_DrainCancelsInFlight proves the framework-side drain:
// an in-flight server-stream blocked on ctx.Done() is actively canceled when the
// server drains, so Close returns promptly instead of blocking on GracefulStop's
// passive wait. Without StreamDrain + the adapter Trigger, Close would hang until
// the watchdog fires.
func TestIntegration_Streaming_DrainCancelsInFlight(t *testing.T) {
	t.Parallel()
	srv, _ := newStreamingServer(t, allMethodsPublic())
	started := make(chan struct{})
	desc := streamerServiceDesc(started)
	integRegisterDesc(t, srv, "grpc.streamer.v1", &desc, new(any))

	lis := newBufconnListener(1024 * 1024)
	// Serve on a background ctx that we never cancel — the drain must come from
	// Close, not from the serve ctx.
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(context.Background(), lis) }()
	waitForServing(t, srv)

	cc := dialBufconn(t, lis)
	defer func() { _ = cc.Close() }()

	// Open the blocking server-stream and consume the first message so the handler
	// is genuinely in-flight (blocked on ctx.Done()) before we drain.
	cs, err := cc.NewStream(context.Background(),
		&grpc.StreamDesc{StreamName: "BlockStream", ServerStreams: true}, streamerBlock)
	require.NoError(t, err)
	require.NoError(t, cs.SendMsg(healthReq()))
	require.NoError(t, cs.RecvMsg(new(grpc_health_v1.HealthCheckResponse)))
	testwait.Deterministic(t, started, "block-stream-handler-in-flight")

	// Close with a background ctx (no budget): only the drain canceling the
	// stream context lets GracefulStop complete. A watchdog catches a hang.
	closeDone := make(chan error, 1)
	go func() { closeDone <- srv.Close(context.Background()) }()
	select {
	case err := <-closeDone:
		require.NoError(t, err, "Close must complete cleanly once the drain cancels the in-flight stream")
	case <-time.After(integServeTimeout):
		t.Fatalf("Close blocked — the framework drain did not cancel the in-flight stream")
	}
	<-serveDone
}
