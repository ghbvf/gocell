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
)

// ─── Config.applyDefaults ────────────────────────────────────────────────────

func TestConfig_ApplyDefaults_ShutdownTimeout(t *testing.T) {
	t.Parallel()
	chain := genIntegChain(t)

	// Zero ShutdownTimeout must be filled by New (applyDefaults is called internally).
	cfg := grpcadapter.Config{
		Addr: ":0",
		TLS: grpcadapter.TLSConfig{
			CertPEM: chain.serverCertPEM,
			KeyPEM:  chain.serverKeyPEM,
		},
	}
	srv, err := grpcadapter.New(cfg)
	require.NoError(t, err)
	require.NotNil(t, srv)
	// We verify indirectly via a successful New — applyDefaults fills ShutdownTimeout
	// so validate() does not error on it; the actual value is verified by integration tests.
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
	_, err := grpcadapter.New(cfg)
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
			_, err := grpcadapter.New(cfg)
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
			_, err := grpcadapter.New(cfg)
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
	_, err := grpcadapter.New(cfg)
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
	srv, err := grpcadapter.New(cfg)
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
	srv, err := grpcadapter.New(cfg)
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
	srv, err := grpcadapter.New(cfg)
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
	srv, err := grpcadapter.New(cfg)
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
	srv, err := grpcadapter.New(cfg)
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
		ShutdownTimeout: 50 * time.Millisecond,
		TLS:             grpcadapter.TLSConfig{AllowInsecure: true},
	}
	srv, err := grpcadapter.New(cfg)
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
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within 2s after ctx deadline exceeded")
	}
}
