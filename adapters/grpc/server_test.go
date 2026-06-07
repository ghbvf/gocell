package grpc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	grpcadapter "github.com/ghbvf/gocell/adapters/grpc"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
)

// withReg injects a fresh shared registrar and drain signal into cfg so New
// satisfies the required Config.Registrar (Option 3 #1152) and Config.Drain
// (PR-10 #1153). These tests do not exercise attribution or drain, so fresh
// instances suffice; the helper is shared across the grpc_test package.
func withReg(cfg grpcadapter.Config) grpcadapter.Config {
	cfg.Registrar = runtimegrpc.NewServiceRegistrar()
	cfg.Drain = runtimegrpc.NewDrainSignal()
	return cfg
}

const (
	// testShutdownTimeout is the ShutdownTimeout used in unit-test configs that
	// exercise the hard-stop path. A short value keeps the test fast; it must be
	// larger than the ctx deadline supplied to Close so graceful drain has no
	// budget and hard stop is forced immediately.
	testShutdownTimeout = testtime.D50ms

	// testCloseWatchdog is the time.After deadline in TestClose_ContextTimeoutForcesHardStop
	// used to detect a hung Close. Large enough to survive any scheduler jitter.
	testCloseWatchdog = testtime.SelectShutdown

	// negativeShutdownTimeout exercises the V1c validation reject path.
	negativeShutdownTimeout = -1 * time.Second
)

// ─── Config.applyDefaults ────────────────────────────────────────────────────

func TestConfig_ApplyDefaults_ShutdownTimeout(t *testing.T) {
	t.Parallel()
	chain := genIntegChain(t)

	// Zero ShutdownTimeout must be accepted: applyDefaults fills it so validate()
	// does not error. A negative value (tested separately in V1c) must be rejected.
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		wantErr bool
	}{
		{"zero-filled-by-defaults", 0, false},
		{"negative-rejected-by-validate", negativeShutdownTimeout, true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := grpcadapter.Config{
				Addr:            ":0",
				ShutdownTimeout: tc.timeout,
				TLS: grpcadapter.TLSConfig{
					CertPEM: chain.serverCertPEM,
					KeyPEM:  chain.serverKeyPEM,
				},
			}
			srv, err := grpcadapter.New(withReg(cfg))
			if tc.wantErr {
				require.Error(t, err)
				require.Nil(t, srv)
			} else {
				require.NoError(t, err)
				require.NotNil(t, srv)
			}
		})
	}
}

// ─── Config.validate ─────────────────────────────────────────────────────────

func TestConfig_Validate_V1_AddrRequired(t *testing.T) {
	t.Parallel()
	chain := genIntegChain(t)

	cfg := grpcadapter.Config{
		Addr: "", // V1: missing
		TLS: grpcadapter.TLSConfig{
			CertPEM: chain.serverCertPEM,
			KeyPEM:  chain.serverKeyPEM,
		},
	}
	_, err := grpcadapter.New(withReg(cfg))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, grpcadapter.ErrAdapterGRPCConfigInvalid, ec.Code)
}

// TestConfig_Validate_V1b_AddrMalformed verifies a syntactically invalid Addr is
// rejected at config time (ErrAdapterGRPCConfigInvalid) rather than surfacing
// later from net.Listen as an infrastructure error.
func TestConfig_Validate_V1b_AddrMalformed(t *testing.T) {
	t.Parallel()
	chain := genIntegChain(t)

	cases := []struct {
		name string
		addr string
	}{
		{name: "no_colon", addr: "localhost"},
		{name: "too_many_colons", addr: "a:b:c:d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := grpcadapter.Config{
				Addr: tc.addr,
				TLS: grpcadapter.TLSConfig{
					CertPEM: chain.serverCertPEM,
					KeyPEM:  chain.serverKeyPEM,
				},
			}
			_, err := grpcadapter.New(withReg(cfg))
			require.Error(t, err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, grpcadapter.ErrAdapterGRPCConfigInvalid, ec.Code)
		})
	}
}

// TestConfig_Validate_V1c_NegativeShutdownTimeout verifies a negative
// ShutdownTimeout is rejected (it would silently degrade every graceful stop
// into a hard Stop). Zero stays valid (applyDefaults fills the default).
func TestConfig_Validate_V1c_NegativeShutdownTimeout(t *testing.T) {
	t.Parallel()
	chain := genIntegChain(t)

	cfg := grpcadapter.Config{
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: negativeShutdownTimeout,
		TLS: grpcadapter.TLSConfig{
			CertPEM: chain.serverCertPEM,
			KeyPEM:  chain.serverKeyPEM,
		},
	}
	_, err := grpcadapter.New(withReg(cfg))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, grpcadapter.ErrAdapterGRPCConfigInvalid, ec.Code)
}

func TestConfig_Validate_V2_AllowInsecureAndCertConflict(t *testing.T) {
	t.Parallel()
	chain := genIntegChain(t)

	tests := []struct {
		name string
		tls  grpcadapter.TLSConfig
	}{
		{
			name: "AllowInsecure_with_CertPEM",
			tls: grpcadapter.TLSConfig{
				AllowInsecure: true,
				CertPEM:       chain.serverCertPEM,
			},
		},
		{
			name: "AllowInsecure_with_KeyPEM",
			tls: grpcadapter.TLSConfig{
				AllowInsecure: true,
				KeyPEM:        chain.serverKeyPEM,
			},
		},
		{
			name: "AllowInsecure_with_ClientCAPEM",
			tls: grpcadapter.TLSConfig{
				AllowInsecure: true,
				ClientCAPEM:   chain.rootCertPEM,
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := grpcadapter.Config{Addr: ":0", TLS: tc.tls}
			_, err := grpcadapter.New(withReg(cfg))
			require.Error(t, err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, grpcadapter.ErrAdapterGRPCConfigInvalid, ec.Code)
		})
	}
}

func TestConfig_Validate_V3V4_TLSCertKeyRequired(t *testing.T) {
	t.Parallel()
	chain := genIntegChain(t)

	tests := []struct {
		name string
		tls  grpcadapter.TLSConfig
	}{
		{
			name: "V3_CertPEM_missing",
			tls: grpcadapter.TLSConfig{
				KeyPEM: chain.serverKeyPEM,
			},
		},
		{
			name: "V4_KeyPEM_missing",
			tls: grpcadapter.TLSConfig{
				CertPEM: chain.serverCertPEM,
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := grpcadapter.Config{Addr: ":0", TLS: tc.tls}
			_, err := grpcadapter.New(withReg(cfg))
			require.Error(t, err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, grpcadapter.ErrAdapterGRPCConfigInvalid, ec.Code)
		})
	}
}

// TestConfig_Validate_V5_FailClosed is the critical security test:
// omitting both AllowInsecure and TLS material must be rejected.
func TestConfig_Validate_V5_FailClosed_NoTLSNoInsecure(t *testing.T) {
	t.Parallel()
	cfg := grpcadapter.Config{
		Addr: ":0",
		TLS:  grpcadapter.TLSConfig{}, // zero value — no TLS, no AllowInsecure
	}
	_, err := grpcadapter.New(withReg(cfg))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, grpcadapter.ErrAdapterGRPCConfigInvalid, ec.Code, "V5 fail-closed must reject zero TLS config")
}

// ─── New — happy paths (three TLS modes) ─────────────────────────────────────

func TestNew_HappyPath_AllowInsecure(t *testing.T) {
	t.Parallel()
	cfg := grpcadapter.Config{
		Addr: ":0",
		TLS:  grpcadapter.TLSConfig{AllowInsecure: true},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)
	require.NotNil(t, srv)
}

func TestNew_HappyPath_ServerTLS(t *testing.T) {
	t.Parallel()
	chain := genIntegChain(t)
	cfg := grpcadapter.Config{
		Addr: ":0",
		TLS: grpcadapter.TLSConfig{
			CertPEM: chain.serverCertPEM,
			KeyPEM:  chain.serverKeyPEM,
		},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)
	require.NotNil(t, srv)
}

func TestNew_HappyPath_MTLS(t *testing.T) {
	t.Parallel()
	chain := genIntegChain(t)
	cfg := grpcadapter.Config{
		Addr: ":0",
		TLS: grpcadapter.TLSConfig{
			CertPEM:     chain.serverCertPEM,
			KeyPEM:      chain.serverKeyPEM,
			ClientCAPEM: chain.rootCertPEM,
		},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)
	require.NotNil(t, srv)
}

// ─── Probes — state transitions ──────────────────────────────────────────────

func TestProbes_NotServingReturnsError(t *testing.T) {
	t.Parallel()
	cfg := grpcadapter.Config{
		Addr: ":0",
		TLS:  grpcadapter.TLSConfig{AllowInsecure: true},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)

	probes := srv.Probes()
	require.Len(t, probes, 1)
	assert.Equal(t, grpcadapter.ProbeReady, probes[0].Name())

	// Not yet serving — probe must report unhealthy.
	probeErr := probes[0].Check(context.Background())
	assert.Error(t, probeErr, "probe must be unhealthy when server has not started serving")
}

// ─── Close — idempotency ──────────────────────────────────────────────────────

func TestClose_IdempotentDoubleCall(t *testing.T) {
	t.Parallel()
	cfg := grpcadapter.Config{
		Addr: ":0",
		TLS:  grpcadapter.TLSConfig{AllowInsecure: true},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)

	ctx := context.Background()
	// Double Close must not panic or deadlock.
	assert.NotPanics(t, func() {
		_ = srv.Close(ctx)
		_ = srv.Close(ctx)
	})
}

func TestClose_ContextTimeoutForcesHardStop(t *testing.T) {
	t.Parallel()
	cfg := grpcadapter.Config{
		Addr:            ":0",
		ShutdownTimeout: testShutdownTimeout,
		TLS:             grpcadapter.TLSConfig{AllowInsecure: true},
	}
	srv, err := grpcadapter.New(withReg(cfg))
	require.NoError(t, err)

	// Close with an already-expired context — must not hang.
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	// Allow the context to expire.
	<-ctx.Done()
	// Close should return quickly (hard stop).
	done := make(chan error, 1)
	go func() { done <- srv.Close(ctx) }()

	select {
	case <-done:
		// OK — returned promptly.
	case <-time.After(testCloseWatchdog):
		t.Fatalf("Close did not return within %v after ctx deadline exceeded", testCloseWatchdog)
	}
}

// ─── New — invalid PEM (V6 buildCredentials error path) ──────────────────────

// TestNew_InvalidPEM verifies that New returns ErrAdapterGRPCTLSConfig when the
// supplied PEM bytes are syntactically invalid. This exercises the buildCredentials
// error aggregation path (V6).
func TestNew_InvalidPEM(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cert []byte
		key  []byte
	}{
		{
			name: "bad_cert_pem",
			cert: []byte("not a pem"),
			key:  []byte("not a pem"),
		},
		{
			name: "bad_cert_only",
			cert: []byte("not a pem"),
			key:  func() []byte { chain := genIntegChain(t); return chain.serverKeyPEM }(),
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := grpcadapter.Config{
				Addr: ":0",
				TLS: grpcadapter.TLSConfig{
					CertPEM: tc.cert,
					KeyPEM:  tc.key,
				},
			}
			_, err := grpcadapter.New(withReg(cfg))
			require.Error(t, err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec), "expected *errcode.Error, got %T: %v", err, err)
			assert.Equal(t, grpcadapter.ErrAdapterGRPCTLSConfig, ec.Code,
				"expected ErrAdapterGRPCTLSConfig from invalid PEM")
		})
	}
}
