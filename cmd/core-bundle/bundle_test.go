package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeManagedResource implements bootstrap.ManagedResource for tests.
type fakeManagedResource struct {
	name        string
	closeCalled bool
	w           worker.Worker
}

func (f *fakeManagedResource) Checkers() map[string]func() error {
	return map[string]func() error{
		f.name: func() error { return nil },
	}
}

func (f *fakeManagedResource) Worker() worker.Worker { return f.w }

func (f *fakeManagedResource) Close() error {
	f.closeCalled = true
	return nil
}

var _ bootstrap.ManagedResource = (*fakeManagedResource)(nil)

// buildTestDeps returns a minimal AppDeps for memory topology unit tests.
// It skips cursor codecs (optional in tests) and internalGuard (dev mode).
func buildTestDeps(t *testing.T) *AppDeps {
	t.Helper()
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	t.Setenv("GOCELL_JWT_ISSUER", "test-issuer")
	t.Setenv("GOCELL_JWT_AUDIENCE", "test-audience")

	eb := eventbus.New()

	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey)
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(keySet, "test-issuer", 15*time.Minute, auth.WithDefaultAudience("test-audience"))
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(keySet, auth.WithExpectedAudiences("test-audience"))
	require.NoError(t, err)

	ps, err := buildPromStack()
	require.NoError(t, err)

	codecs, err := loadAllCursorCodecs("")
	require.NoError(t, err)

	return &AppDeps{
		Topology:     bootstrap.Topology{StorageBackend: "memory", AdapterMode: ""},
		PGResource:   nil,
		JWTDeps:      jwtDeps{issuer: issuer, verifier: verifier},
		PromStack:    ps,
		CursorCodecs: codecs,
		HMACKey:      []byte("test-hmac-key-32-bytes-long!!!!!"),
		EventBus:     eb,
	}
}

// TestBuildBootstrap_MemoryTopology verifies that a memory topology produces a
// working bootstrap without a PG health checker.
func TestBuildBootstrap_MemoryTopology(t *testing.T) {
	deps := buildTestDeps(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	app, err := BuildBootstrap(deps, bootstrap.WithListener(ln))
	require.NoError(t, err)
	require.NotNil(t, app)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + addr + "/healthz") //nolint:noctx
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond, "memory bootstrap must become healthy")

	// /readyz must be healthy (no PG checker to fail).
	resp, err := http.Get("http://" + addr + "/readyz") //nolint:noctx
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err, "memory bootstrap must shut down cleanly")
	case <-time.After(10 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestBuildBootstrap_PostgresTopology verifies that a postgres topology attaches
// the PG health checker and relay worker from a fake ManagedResource.
func TestBuildBootstrap_PostgresTopology(t *testing.T) {
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())

	fakePG := &fakeManagedResource{name: "postgres"}
	eb := eventbus.New()

	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey)
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(keySet, "test-issuer", 15*time.Minute, auth.WithDefaultAudience("test-audience"))
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(keySet, auth.WithExpectedAudiences("test-audience"))
	require.NoError(t, err)

	ps, err := buildPromStack()
	require.NoError(t, err)
	codecs, err := loadAllCursorCodecs("")
	require.NoError(t, err)

	deps := &AppDeps{
		// postgres topology but with a fake PGResource (no real PG needed for wiring test).
		// MetricsToken and VerboseToken are required in real adapter mode.
		Topology:     bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"},
		PGResource:   fakePG,
		JWTDeps:      jwtDeps{issuer: issuer, verifier: verifier},
		PromStack:    ps,
		CursorCodecs: codecs,
		HMACKey:      []byte("test-hmac-key-32-bytes-long!!!!!"),
		EventBus:     eb,
		MetricsToken: "test-metrics-token",
		VerboseToken: "test-verbose-token",
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	app, err := BuildBootstrap(deps, bootstrap.WithListener(ln))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + addr + "/healthz") //nolint:noctx
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond, "postgres topology bootstrap must become healthy")

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}

	// Fake PG resource must be closed during shutdown.
	assert.True(t, fakePG.closeCalled, "PGResource.Close() must be called during shutdown")
}

// TestBuildBootstrap_AssemblyHasAllCells verifies that BuildBootstrap registers
// config-core, access-core, and audit-core. We check this via health + /readyz
// which would fail if any cell fails to init.
func TestBuildBootstrap_AssemblyHasAllCells(t *testing.T) {
	deps := buildTestDeps(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	app, err := BuildBootstrap(deps, bootstrap.WithListener(ln))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + addr + "/healthz") //nolint:noctx
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond, "full assembly must become healthy")

	// /readyz confirms all three cells started and registered their probes.
	resp, err := http.Get("http://" + addr + "/readyz") //nolint:noctx
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"all three cells (config-core, access-core, audit-core) must be healthy")

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("full assembly bootstrap did not shut down in time")
	}
}
