package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	accesscore "github.com/ghbvf/gocell/cells/access-core"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastAdminBootstrapOpts returns access-core InitialAdmin options that
// replace the production bcrypt cost=12 hasher with bcrypt.MinCost=4 so
// the synchronous bcrypt call in access-core.Init does not block phase3
// for 5-7s on slow CI runners. The rest of the InitialAdmin path
// (Sweep → EnsureAdmin → WriteCredentialFile → Cleaner worker registration)
// still runs, preserving bundle_test coverage of the full wiring.
func fastAdminBootstrapOpts() []accesscore.InitialAdminOption {
	return []accesscore.InitialAdminOption{
		accesscore.WithBootstrapPasswordHasher(accesscore.BcryptHasher{Cost: bcrypt.MinCost}),
	}
}

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
		Topology:                  bootstrap.Topology{StorageBackend: "memory", AdapterMode: ""},
		PGResource:                nil,
		JWTDeps:                   jwtDeps{issuer: issuer, verifier: verifier},
		PromStack:                 ps,
		CursorCodecs:              codecs,
		HMACKey:                   []byte("test-hmac-key-32-bytes-long!!!!!"),
		EventBus:                  eb,
		InitialAdminBootstrapOpts: fastAdminBootstrapOpts(),
	}
}

// newValidatedAppDeps returns an AppDeps that passes Validate() for the given
// topology. Test cases can then mutate individual fields to assert that a
// single missing field surfaces the expected error.
func newValidatedAppDeps(t *testing.T, topo bootstrap.Topology) *AppDeps {
	t.Helper()
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())

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
		Topology:     topo,
		JWTDeps:      jwtDeps{issuer: issuer, verifier: verifier},
		PromStack:    ps,
		CursorCodecs: codecs,
		HMACKey:      []byte("test-hmac-key-32-bytes-long!!!!!"),
		EventBus:     eventbus.New(),
	}
	if topo.RequireProductionControlPlane() {
		deps.PGResource = &fakeManagedResource{name: "postgres"}
		deps.MetricsToken = "test-metrics"
		deps.VerboseToken = "test-verbose"
		deps.InternalGuard = func(h http.Handler) http.Handler { return h }
	}
	return deps
}

// TestAppDeps_Validate covers every invariant enforced by AppDeps.Validate.
// Each case takes a baseline that passes Validate and mutates one field to
// verify Validate surfaces that specific failure with errcode.ErrValidationFailed.
// This is the contract BuildBootstrap depends on.
func TestAppDeps_Validate(t *testing.T) {
	prodTopo := bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"}
	devTopo := bootstrap.Topology{StorageBackend: "memory", AdapterMode: ""}

	cases := []struct {
		name       string
		topo       bootstrap.Topology
		mutate     func(*AppDeps)
		wantErr    bool
		wantSubstr string // required substring in one of the joined errors
	}{
		{name: "prod baseline is valid", topo: prodTopo, mutate: func(*AppDeps) {}, wantErr: false},
		{name: "dev baseline is valid", topo: devTopo, mutate: func(*AppDeps) {}, wantErr: false},

		{name: "missing JWT issuer", topo: devTopo, mutate: func(d *AppDeps) { d.JWTDeps.issuer = nil }, wantErr: true, wantSubstr: "JWTDeps.issuer"},
		{name: "missing JWT verifier", topo: devTopo, mutate: func(d *AppDeps) { d.JWTDeps.verifier = nil }, wantErr: true, wantSubstr: "JWTDeps.verifier"},
		{name: "missing prom registry", topo: devTopo, mutate: func(d *AppDeps) { d.PromStack.registry = nil }, wantErr: true, wantSubstr: "PromStack.registry"},
		{name: "missing prom hook observer", topo: devTopo, mutate: func(d *AppDeps) { d.PromStack.hookObserver = nil }, wantErr: true, wantSubstr: "PromStack.hookObserver"},
		{name: "missing prom metric provider", topo: devTopo, mutate: func(d *AppDeps) { d.PromStack.metricProvider = nil }, wantErr: true, wantSubstr: "PromStack.metricProvider"},
		{name: "missing cursor codec audit", topo: devTopo, mutate: func(d *AppDeps) { d.CursorCodecs.audit = nil }, wantErr: true, wantSubstr: "CursorCodecs.audit"},
		{name: "missing cursor codec config", topo: devTopo, mutate: func(d *AppDeps) { d.CursorCodecs.config = nil }, wantErr: true, wantSubstr: "CursorCodecs.config"},
		{name: "missing HMAC key", topo: devTopo, mutate: func(d *AppDeps) { d.HMACKey = nil }, wantErr: true, wantSubstr: "HMACKey"},
		{name: "missing event bus", topo: devTopo, mutate: func(d *AppDeps) { d.EventBus = nil }, wantErr: true, wantSubstr: "EventBus"},

		{name: "prod missing verbose token", topo: prodTopo, mutate: func(d *AppDeps) { d.VerboseToken = "" }, wantErr: true, wantSubstr: "GOCELL_READYZ_VERBOSE_TOKEN"},
		{name: "prod missing metrics token", topo: prodTopo, mutate: func(d *AppDeps) { d.MetricsToken = "" }, wantErr: true, wantSubstr: "GOCELL_METRICS_TOKEN"},
		{name: "prod missing internal guard", topo: prodTopo, mutate: func(d *AppDeps) { d.InternalGuard = nil }, wantErr: true, wantSubstr: "GOCELL_SERVICE_SECRET"},
		{name: "prod missing PGResource", topo: prodTopo, mutate: func(d *AppDeps) { d.PGResource = nil }, wantErr: true, wantSubstr: "PGResource"},

		{name: "dev with stray PGResource", topo: devTopo, mutate: func(d *AppDeps) { d.PGResource = &fakeManagedResource{name: "stray"} }, wantErr: true, wantSubstr: "PGResource must be nil"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newValidatedAppDeps(t, tc.topo)
			tc.mutate(deps)

			err := deps.Validate()
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSubstr, "error must mention the offending field")
			// Every joined error must still carry the validation code so callers
			// classify startup failures uniformly.
			for _, sub := range allJoinedErrors(err) {
				var ec *errcode.Error
				require.ErrorAs(t, sub, &ec, "joined error %v must be *errcode.Error", sub)
				assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
			}
		})
	}
}

// TestAppDeps_Validate_NilReceiver covers the defensive nil-receiver case so
// a mistakenly nil deps argument fails explicitly rather than nil-deref'ing.
func TestAppDeps_Validate_NilReceiver(t *testing.T) {
	var deps *AppDeps
	err := deps.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil receiver")
}

// TestBuildBootstrap_RejectsInvalidDeps guards the contract that BuildBootstrap
// applies Validate before constructing any cell. A deps value that violates
// Validate must surface an error without side effects.
func TestBuildBootstrap_RejectsInvalidDeps(t *testing.T) {
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	deps := newValidatedAppDeps(t, bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"})
	deps.VerboseToken = "" // violate prod invariant

	app, err := BuildBootstrap(deps)
	require.Error(t, err)
	assert.Nil(t, app)
	assert.Contains(t, err.Error(), "GOCELL_READYZ_VERBOSE_TOKEN")
}

// allJoinedErrors walks an errors.Join tree and returns leaves that are not
// themselves joined. Used to assert every leaf error is an *errcode.Error.
func allJoinedErrors(err error) []error {
	type unwrapper interface{ Unwrap() []error }
	if u, ok := err.(unwrapper); ok {
		var out []error
		for _, e := range u.Unwrap() {
			out = append(out, allJoinedErrors(e)...)
		}
		return out
	}
	return []error{err}
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
		// Production topology requires MetricsToken/VerboseToken/InternalGuard per AppDeps.Validate().
		Topology:     bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"},
		PGResource:   fakePG,
		JWTDeps:      jwtDeps{issuer: issuer, verifier: verifier},
		PromStack:    ps,
		CursorCodecs: codecs,
		HMACKey:      []byte("test-hmac-key-32-bytes-long!!!!!"),
		EventBus:     eb,
		MetricsToken: "test-metrics-token",
		VerboseToken: "test-verbose-token",
		InternalGuard: func(h http.Handler) http.Handler {
			return h // identity guard for the wiring test
		},
		InitialAdminBootstrapOpts: fastAdminBootstrapOpts(),
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
