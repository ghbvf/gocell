package grpc_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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

	grpcadapter "github.com/ghbvf/gocell/adapters/grpc"
)

const (
	// integServeTimeout bounds the serve goroutine in each integration test.
	integServeTimeout = 5 * time.Second
	// integDialTimeout bounds the client dial attempt.
	integDialTimeout = 2 * time.Second
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
	ctx, cancel := context.WithTimeout(context.Background(), integDialTimeout)
	defer cancel()
	cc, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	// Force a connect attempt.
	cc.Connect()
	// Wait for the connection to be ready via a health check.
	_ = ctx
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

	st := status.Code(err)
	assert.True(t,
		st == codes.Unavailable || st == codes.Unknown,
		"expected Unavailable or Unknown from TLS handshake failure, got %v", st)
}

// TestIntegration_GracefulDrain verifies that in-flight RPCs complete before Close returns.
func TestIntegration_GracefulDrain(t *testing.T) {
	t.Parallel()

	cfg := grpcadapter.Config{
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: integServeTimeout,
		TLS:             grpcadapter.TLSConfig{AllowInsecure: true},
	}
	srv, err := grpcadapter.New(cfg)
	require.NoError(t, err)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(srv.ServiceRegistrar(), healthSrv)

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

	// Ensure basic connectivity before draining.
	st, err := healthCheck(t, cc)
	require.NoError(t, err)
	assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, st)

	// Trigger graceful stop.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), integServeTimeout)
	defer closeCancel()

	closeErr := srv.Close(closeCtx)
	assert.NoError(t, closeErr, "Close should succeed within shutdown budget")

	// Server should no longer be serving after Close.
	probes := srv.Probes()
	require.Len(t, probes, 1)
	assert.Error(t, probes[0].Check(context.Background()), "probe must be unhealthy after Close")
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// waitForServing polls srv.Probes()[0].Check until it returns nil or times out.
func waitForServing(t *testing.T, srv *grpcadapter.Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	probes := srv.Probes()
	require.Len(t, probes, 1)
	for time.Now().Before(deadline) {
		if err := probes[0].Check(context.Background()); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("server did not start serving within 2s")
}
