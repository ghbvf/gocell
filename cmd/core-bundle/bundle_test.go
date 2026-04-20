package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	accesscore "github.com/ghbvf/gocell/cells/access-core"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	kworker "github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
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

// fakeManagedResource implements lifecycle.ManagedResource for tests.
type fakeManagedResource struct {
	name        string
	closeCalled bool
	w           kworker.Worker
}

func (f *fakeManagedResource) Checkers() map[string]func() error {
	return map[string]func() error{
		f.name: func() error { return nil },
	}
}

func (f *fakeManagedResource) Worker() kworker.Worker { return f.w }

func (f *fakeManagedResource) Close(_ context.Context) error {
	f.closeCalled = true
	return nil
}

var _ kernellifecycle.ManagedResource = (*fakeManagedResource)(nil)

// buildTestSharedDeps returns a minimal SharedDeps for memory topology tests.
// It skips cursor codecs (optional in tests) and internalGuard (dev mode).
func buildTestSharedDeps(t *testing.T) *SharedDeps {
	t.Helper()
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	t.Setenv("GOCELL_JWT_ISSUER", "test-issuer")
	t.Setenv("GOCELL_JWT_AUDIENCE", "test-audience")

	eb := eventbus.New()

	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey)
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(keySet, "test-issuer", 15*time.Minute, auth.WithIssuerAudiencesFromSlice([]string{"test-audience"}))
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(keySet, auth.WithExpectedAudiences("test-audience"))
	require.NoError(t, err)

	ps, err := buildPromStack()
	require.NoError(t, err)

	codecs, err := loadAllCursorCodecs("")
	require.NoError(t, err)

	return &SharedDeps{
		Topology:     bootstrap.Topology{StorageBackend: "memory", AdapterMode: ""},
		JWTDeps:      jwtDeps{issuer: issuer, verifier: verifier},
		PromStack:    ps,
		CursorCodecs: codecs,
		HMACKey:      []byte("test-hmac-key-32-bytes-long!!!!!"),
		EventBus:     eb,
	}
}

// newValidatedSharedDeps returns a SharedDeps that passes Validate() for the
// given topology. Test cases can mutate individual fields to assert that a
// single missing field surfaces the expected error.
//
// Note: PGResource is no longer part of SharedDeps; it is built inside
// ConfigCoreModule.Provide. The "prod baseline" topology is tested here
// without PGResource — the prod storage gate is now in ConfigCoreModule.
func newValidatedSharedDeps(t *testing.T, topo bootstrap.Topology) *SharedDeps {
	t.Helper()
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())

	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey)
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(keySet, "test-issuer", 15*time.Minute, auth.WithIssuerAudiencesFromSlice([]string{"test-audience"}))
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(keySet, auth.WithExpectedAudiences("test-audience"))
	require.NoError(t, err)

	ps, err := buildPromStack()
	require.NoError(t, err)
	codecs, err := loadAllCursorCodecs("")
	require.NoError(t, err)

	deps := &SharedDeps{
		Topology:     topo,
		JWTDeps:      jwtDeps{issuer: issuer, verifier: verifier},
		PromStack:    ps,
		CursorCodecs: codecs,
		HMACKey:      []byte("test-hmac-key-32-bytes-long!!!!!"),
		EventBus:     eventbus.New(),
	}
	if topo.RequireProductionControlPlane() {
		deps.MetricsToken = "test-metrics"
		deps.VerboseToken = "test-verbose"
		deps.InternalGuard = func(h http.Handler) http.Handler { return h }
	}
	if topo.StorageBackend == "postgres" {
		t.Setenv("GOCELL_KEY_PROVIDER", "local-aes")
		t.Setenv("GOCELL_MASTER_KEY", "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")
	}
	return deps
}

// buildBootstrapFromShared is the test-path assembly helper, equivalent to the
// production run() flow but accepts extra bootstrap.Options (e.g. WithListener).
// Uses memory topology (modules' Provide path) and AccessCoreModule with a
// fast-bcrypt option.
func buildBootstrapFromShared(t *testing.T, shared *SharedDeps, extra ...bootstrap.Option) (*bootstrap.Bootstrap, error) {
	t.Helper()
	ctx := context.Background()

	cells, cellOpts, err := BuildApp(ctx, shared,
		ConfigCoreModule{},
		AccessCoreModule{InitialAdminOpts: fastAdminBootstrapOpts()},
		AuditCoreModule{},
	)
	if err != nil {
		return nil, err
	}

	asm, err := buildAssembly(shared.PromStack, cells...)
	if err != nil {
		return nil, err
	}

	consumerBase, err := buildConsumerBase()
	if err != nil {
		return nil, err
	}

	metricsHandler, err := buildMetricsHandler(shared.MetricsToken, shared.PromStack.registry)
	if err != nil {
		return nil, err
	}

	adapterInfo := shared.Topology.AdapterInfo()
	opts := defaultRuntimeOptions(shared, asm, consumerBase, metricsHandler, adapterInfo)
	opts = append(opts, cellOpts...)
	opts = append(opts, extra...)
	return bootstrap.New(opts...), nil
}

// TestSharedDeps_Validate covers every invariant enforced by SharedDeps.Validate.
// Each case takes a baseline that passes Validate and mutates one field to
// verify Validate surfaces that specific failure with errcode.ErrValidationFailed.
func TestSharedDeps_Validate(t *testing.T) {
	prodTopo := bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"}
	devTopo := bootstrap.Topology{StorageBackend: "memory", AdapterMode: ""}

	cases := []struct {
		name       string
		topo       bootstrap.Topology
		mutate     func(*SharedDeps)
		wantErr    bool
		wantSubstr string // required substring in one of the joined errors
	}{
		{name: "prod baseline is valid", topo: prodTopo, mutate: func(*SharedDeps) {}, wantErr: false},
		{name: "dev baseline is valid", topo: devTopo, mutate: func(*SharedDeps) {}, wantErr: false},

		{name: "missing JWT issuer", topo: devTopo, mutate: func(d *SharedDeps) { d.JWTDeps.issuer = nil }, wantErr: true, wantSubstr: "JWTDeps.issuer"},
		{name: "missing JWT verifier", topo: devTopo, mutate: func(d *SharedDeps) { d.JWTDeps.verifier = nil }, wantErr: true, wantSubstr: "JWTDeps.verifier"},
		{name: "missing prom registry", topo: devTopo, mutate: func(d *SharedDeps) { d.PromStack.registry = nil }, wantErr: true, wantSubstr: "PromStack.registry"},
		{name: "missing prom hook observer", topo: devTopo, mutate: func(d *SharedDeps) { d.PromStack.hookObserver = nil }, wantErr: true, wantSubstr: "PromStack.hookObserver"},
		{name: "missing prom metric provider", topo: devTopo, mutate: func(d *SharedDeps) { d.PromStack.metricProvider = nil }, wantErr: true, wantSubstr: "PromStack.metricProvider"},
		{name: "missing cursor codec audit", topo: devTopo, mutate: func(d *SharedDeps) { d.CursorCodecs.audit = nil }, wantErr: true, wantSubstr: "CursorCodecs.audit"},
		{name: "missing cursor codec config", topo: devTopo, mutate: func(d *SharedDeps) { d.CursorCodecs.config = nil }, wantErr: true, wantSubstr: "CursorCodecs.config"},
		{name: "missing HMAC key", topo: devTopo, mutate: func(d *SharedDeps) { d.HMACKey = nil }, wantErr: true, wantSubstr: "HMACKey"},
		{name: "missing event bus", topo: devTopo, mutate: func(d *SharedDeps) { d.EventBus = nil }, wantErr: true, wantSubstr: "EventBus"},

		{name: "prod missing verbose token", topo: prodTopo, mutate: func(d *SharedDeps) { d.VerboseToken = "" }, wantErr: true, wantSubstr: "GOCELL_READYZ_VERBOSE_TOKEN"},
		{name: "prod missing metrics token", topo: prodTopo, mutate: func(d *SharedDeps) { d.MetricsToken = "" }, wantErr: true, wantSubstr: "GOCELL_METRICS_TOKEN"},
		{name: "prod missing internal guard", topo: prodTopo, mutate: func(d *SharedDeps) { d.InternalGuard = nil }, wantErr: true, wantSubstr: "GOCELL_SERVICE_SECRET"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newValidatedSharedDeps(t, tc.topo)
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

// TestSharedDeps_Validate_NilReceiver covers the defensive nil-receiver case.
func TestSharedDeps_Validate_NilReceiver(t *testing.T) {
	var deps *SharedDeps
	err := deps.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil receiver")
}

// TestBuildApp_RejectsInvalidSharedDeps guards that BuildApp propagates
// SharedDeps.Validate() failure before constructing any cell.
func TestBuildApp_RejectsInvalidSharedDeps(t *testing.T) {
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	deps := newValidatedSharedDeps(t, bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"})
	deps.VerboseToken = "" // violate prod invariant

	_, _, err := BuildApp(context.Background(), deps,
		ConfigCoreModule{},
		AccessCoreModule{},
		AuditCoreModule{},
	)
	require.Error(t, err)
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
	shared := buildTestSharedDeps(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	app, err := buildBootstrapFromShared(t, shared, bootstrap.WithListener(ln))
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

// TestBuildBootstrap_PostgresTopology_FakePGResource verifies that a postgres
// topology with a fake ManagedResource wired via WithManagedResource option
// attaches the PG health checker and calls Close on shutdown.
//
// In the new CellModule model, ConfigCoreModule.Provide would build the real
// PGResource from env. This test injects a fake by passing it as an extra
// bootstrap.Option, exercising the ManagedResource lifecycle path directly.
//
// Note: despite the name, this test does NOT exercise the Postgres code path —
// StorageBackend is fixed to "memory". The test name is historical. Its sole
// purpose is verifying the WithManagedResource lifecycle hooks
// (Checkers / Worker / Close).
func TestBuildBootstrap_PostgresTopology_FakePGResource(t *testing.T) {
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())

	shared := buildTestSharedDeps(t)
	shared.Topology = bootstrap.Topology{StorageBackend: "memory", AdapterMode: ""}

	fakePG := &fakeManagedResource{name: "fake-postgres"}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	app, err := buildBootstrapFromShared(t, shared,
		bootstrap.WithListener(ln),
		bootstrap.WithManagedResource(fakePG),
	)
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
	}, 5*time.Second, 50*time.Millisecond, "bootstrap with fake PG must become healthy")

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}

	// Fake PG resource must be closed during shutdown.
	assert.True(t, fakePG.closeCalled, "fakeManagedResource.Close() must be called during shutdown")
}

// TestBuildBootstrap_AssemblyHasAllCells verifies that BuildApp registers
// config-core, access-core, and audit-core. We check via health + /readyz
// which would fail if any cell fails to init.
func TestBuildBootstrap_AssemblyHasAllCells(t *testing.T) {
	shared := buildTestSharedDeps(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	app, err := buildBootstrapFromShared(t, shared, bootstrap.WithListener(ln))
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
