package grpc_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
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
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
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
		done <- srv.ServeListenerForTest(ctx, lis)
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

// ─── Integration tests ────────────────────────────────────────────────────────

// TestIntegration_Plaintext_BufconnCheck verifies plaintext gRPC via bufconn.
func TestIntegration_Plaintext_BufconnCheck(t *testing.T) {
	t.Parallel()

	cfg := grpcadapter.Config{
		Addr:            ":0",
		ShutdownTimeout: integServeTimeout,
		TLS:             grpcadapter.TLSConfig{AllowInsecure: true},
	}
	srv, err := grpcadapter.New(cfg)
	require.NoError(t, err)

	// Register health service so the RPC call has something to hit.
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(srv.ServiceRegistrar(), healthSrv)

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
	srv, err := grpcadapter.New(cfg)
	require.NoError(t, err)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(srv.ServiceRegistrar(), healthSrv)

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
	srv, err := grpcadapter.New(cfg)
	require.NoError(t, err)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(srv.ServiceRegistrar(), healthSrv)

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
	srv, err := grpcadapter.New(cfg)
	require.NoError(t, err)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(srv.ServiceRegistrar(), healthSrv)

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
	srv, err := grpcadapter.New(cfg)
	require.NoError(t, err)

	rpcStarted := make(chan struct{})
	release := make(chan struct{})
	desc := blockingServiceDesc(rpcStarted, release)
	srv.ServiceRegistrar().RegisterService(&desc, new(any))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()

	serveCtx, serveCancel := context.WithTimeout(context.Background(), integServeTimeout)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- srv.ServeListenerForTest(serveCtx, lis)
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
	testwait.Deterministic(t, rpcStarted, integServeTimeout, "blocking-rpc-started")

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
	closeErr := testwait.Deterministic(t, closeReturned, integServeTimeout, "close-returns-after-drain")
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
	srv, err := grpcadapter.New(cfg)
	require.NoError(t, err)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	serveCtx, serveCancel := context.WithTimeout(context.Background(), integServeTimeout)
	defer serveCancel()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- srv.ServeListenerForTest(serveCtx, lis)
	}()

	waitForServing(t, srv)

	// Fire Worker().Stop() and Close() simultaneously from two goroutines.
	teardownCtx, teardownCancel := context.WithTimeout(context.Background(), integServeTimeout)
	defer teardownCancel()

	stopDone := make(chan error, 1)
	closeDone := make(chan error, 1)
	go func() { stopDone <- srv.Worker().Stop(teardownCtx) }()
	go func() { closeDone <- srv.Close(teardownCtx) }()

	stopErr := testwait.Deterministic(t, stopDone, integServeTimeout, "worker-stop")
	closeErr := testwait.Deterministic(t, closeDone, integServeTimeout, "close")

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
// branch: when the context passed to ServeListenerForTest is canceled (simulating
// a WorkerGroup / bootstrap external cancel), serve initiates a graceful drain
// using its own self-owned ShutdownTimeout budget (via context.WithoutCancel +
// cfg.ShutdownTimeout), then returns ctx.Err() == context.Canceled.
//
// This is the only integration path NOT covered by the Close / Worker().Stop
// family of tests. It exercises the mirror of the runtime/websocket Hub
// external-cancel shutdown pattern documented in server.go.
func TestIntegration_ServeContextCancel_GracefulDrain(t *testing.T) {
	t.Parallel()

	cfg := grpcadapter.Config{
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: integServeTimeout,
		TLS:             grpcadapter.TLSConfig{AllowInsecure: true},
	}
	srv, err := grpcadapter.New(cfg)
	require.NoError(t, err)

	// Register health service so waitForServing has an RPC to probe against.
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(srv.ServiceRegistrar(), healthSrv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()

	// cancelableCtx simulates the WorkerGroup / bootstrap parent ctx being
	// canceled externally (sibling worker crash, SIGTERM, etc.).
	cancelableCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- srv.ServeListenerForTest(cancelableCtx, lis)
	}()

	// Wait until the server is actively serving before triggering the cancel.
	waitForServing(t, srv)

	// Sanity: one successful RPC confirms the server is actually serving RPCs,
	// not just that the probe flipped to healthy.
	cc := dialInsecure(t, addr)
	defer func() { _ = cc.Close() }()
	st, err := healthCheck(t, cc)
	require.NoError(t, err)
	assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, st,
		"server must respond to health check before ctx cancel")

	// Cancel the context — this is the core of this test: external cancel of
	// the serve ctx triggers the ctx.Done branch in serve().
	cancel()

	// serve() must return context.Canceled (the ctx.Err() from the canceled ctx).
	serveErr := testwait.Deterministic(t, serveDone, integServeTimeout, "serve-returns-on-ctx-cancel")
	assert.True(t, errors.Is(serveErr, context.Canceled),
		"serve must return context.Canceled on external ctx cancel, got: %v", serveErr)

	// After serve returns, gracefulStop has completed and serving must be false.
	probes := srv.Probes()
	require.Len(t, probes, 1)
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
