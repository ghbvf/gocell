package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/initialadmin"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	kworker "github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestInternalGuard constructs an internalGuard backed by an
// InMemoryNonceStore so prod-topology SharedDeps.Validate paths see a
// replay-safe store (NonceStoreKindInMemory) rather than a Noop.
func newTestInternalGuard(t *testing.T) *internalGuard {
	t.Helper()
	ring, err := auth.NewHMACKeyRing([]byte("test-secret-32-bytes-long-padding!"), nil)
	require.NoError(t, err)
	store, err := auth.NewInMemoryNonceStore(auth.ServiceTokenMaxAge + nonceStoreBuffer)
	require.NoError(t, err)
	return &internalGuard{
		ring:       ring,
		nonceStore: store,
		mw:         func(h http.Handler) http.Handler { return h },
	}
}

// fastAdminBootstrapOpts returns accesscore LifecycleOptions that
// replace the production bcrypt cost=12 hasher with bcrypt.MinCost=4 so
// the synchronous bcrypt call in accesscore.Init does not block phase3
// for 5-7s on slow CI runners. The rest of the InitialAdmin path
// (Sweep → EnsureAdmin → WriteCredentialFile → Cleaner worker registration)
// still runs, preserving bundle_test coverage of the full wiring.
func fastAdminBootstrapOpts() []initialadmin.LifecycleOption {
	return []initialadmin.LifecycleOption{
		initialadmin.WithPasswordHasher(initialadmin.BcryptHasher{Cost: bcrypt.MinCost}),
	}
}

// fakeManagedResource implements lifecycle.ManagedResource for tests.
type fakeManagedResource struct {
	name        string
	closeCalled bool
	w           kworker.Worker
}

func (f *fakeManagedResource) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		f.name: func(context.Context) error { return nil },
	}
}

func (f *fakeManagedResource) Worker() kworker.Worker { return f.w }

func (f *fakeManagedResource) Close(_ context.Context) error {
	f.closeCalled = true
	return nil
}

var _ kernellifecycle.ManagedResource = (*fakeManagedResource)(nil)

// buildTestSharedDeps returns a minimal SharedDeps for memory topology tests.
// Cell-specific keys (cursor codecs, HMAC key) are now module-private and are
// read from the environment by each CellModule.Provide at wiring time.
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

	return &SharedDeps{
		Topology:  bootstrap.Topology{StorageBackend: "memory", AdapterMode: ""},
		JWTDeps:   jwtDeps{issuer: issuer, verifier: verifier},
		PromStack: ps,
		EventBus:  eb,
		// PR-A35: verbose endpoint is gated in every mode. Memory/dev tests
		// just waive it — nothing here exercises the verbose body.
		VerboseDisabled: true,
		// PR-A14a: PrimaryHTTPAddr/InternalHTTPAddr left empty. Tests that
		// drive the full BuildApp path must inject listeners via
		// WithPrimaryListener + WithInternalListener so bind addrs are
		// unused; phase0 accepts either an addr or a listener per side.
	}
}

// newValidatedSharedDeps returns a SharedDeps that passes Validate() for the
// given topology. Test cases can mutate individual fields to assert that a
// single missing field surfaces the expected error.
//
// Note: PGResource, cursor codecs, HMAC key, and KeyProvider are no longer
// part of SharedDeps; they are built inside the respective CellModule.Provide.
// The "prod baseline" topology is tested here without those fields — the cell
// module gates are now in each module's Provide.
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

	deps := &SharedDeps{
		Topology:  topo,
		JWTDeps:   jwtDeps{issuer: issuer, verifier: verifier},
		PromStack: ps,
		EventBus:  eventbus.New(),
		// PR-A35: verbose endpoint is now gated in every mode. A test-time
		// token keeps the dev baseline valid; prod tests override via the
		// mutate callback when they want to exercise the missing-token path.
		VerboseToken: "test-verbose",
		// PR-A14a: addrs intentionally empty; tests drive via listener injection.
	}
	if topo.RequireProductionControlPlane() {
		deps.MetricsToken = "test-metrics"
		deps.InternalGuard = newTestInternalGuard(t)
	}
	if topo.StorageBackend == "postgres" {
		t.Setenv("GOCELL_CONFIGCORE_MASTER_KEY", "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")
	}
	return deps
}

func TestDurabilityModeForTopology_UsesStorageBackend(t *testing.T) {
	tests := []struct {
		name string
		topo bootstrap.Topology
		want cell.DurabilityMode
	}{
		{
			name: "memory real remains demo",
			topo: bootstrap.Topology{StorageBackend: "memory", AdapterMode: "real"},
			want: cell.DurabilityDemo,
		},
		{
			name: "postgres real is durable",
			topo: bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"},
			want: cell.DurabilityDurable,
		},
		{
			name: "memory dev remains demo",
			topo: bootstrap.Topology{StorageBackend: "memory", AdapterMode: "dev"},
			want: cell.DurabilityDemo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, durabilityModeForTopology(tt.topo))
		})
	}
}

// buildBootstrapFromShared is the test-path assembly helper, equivalent to the
// production run() flow. It owns the PrimaryListener registration so the JWT
// policy (PolicyJWTFromAssembly) is wired with the assembly that BuildApp
// constructs internally. Tests supply the primary net.Listener and any extra
// options (typically WithListener for InternalListener/HealthListener,
// WithManagedResource, etc.). Uses memory topology and AccessCoreModule with
// a fast-bcrypt option.
func buildBootstrapFromShared(t *testing.T, shared *SharedDeps, primaryLn net.Listener, extra ...bootstrap.Option) (*bootstrap.Bootstrap, error) {
	t.Helper()
	ctx := context.Background()

	cells, cellOpts, err := BuildApp(ctx, shared,
		ConfigCoreModule{},
		AccessCoreModule{ForceBootstrap: true, InitialAdminOpts: fastAdminBootstrapOpts()},
		AuditCoreModule{},
	)
	if err != nil {
		return nil, err
	}

	asm, err := buildAssembly(shared.PromStack, durabilityModeForTopology(shared.Topology), cells...)
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
	// Primary listener carries the JWT policy resolved from the assembly. F3
	// round-3: this is the single source of truth for JWT auth — there is no
	// longer a standalone bootstrap.PolicyJWTFromAssembly Option.
	opts = append(opts, bootstrap.WithListener(
		cell.PrimaryListener,
		primaryLn.Addr().String(),
		bootstrap.PolicyJWTFromAssembly(asm),
		bootstrap.WithListenerNet(primaryLn),
	))
	opts = append(opts, extra...)
	return bootstrap.New(opts...), nil
}

// TestSharedDeps_Validate covers every invariant enforced by SharedDeps.Validate.
// Each case takes a baseline that passes Validate and mutates one field to
// verify Validate surfaces that specific failure with errcode.ErrValidationFailed.
func TestSharedDeps_Validate(t *testing.T) {
	// SinglePodReplayProtection=true acknowledges in-memory replay defense scope
	// for single-pod deployments (mirrors GOCELL_SINGLE_POD=1).
	prodTopo := bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real", SinglePodReplayProtection: true}
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
		{name: "missing event bus", topo: devTopo, mutate: func(d *SharedDeps) { d.EventBus = nil }, wantErr: true, wantSubstr: "EventBus"},

		{name: "prod missing verbose token", topo: prodTopo, mutate: func(d *SharedDeps) { d.VerboseToken = "" }, wantErr: true, wantSubstr: "GOCELL_READYZ_VERBOSE_TOKEN"},
		{name: "dev missing verbose token", topo: devTopo, mutate: func(d *SharedDeps) { d.VerboseToken = "" }, wantErr: true, wantSubstr: "GOCELL_READYZ_VERBOSE_TOKEN"},
		{
			name:    "dev with verbose disabled flag is valid",
			topo:    devTopo,
			mutate:  func(d *SharedDeps) { d.VerboseToken = ""; d.VerboseDisabled = true },
			wantErr: false,
		},
		{
			name:       "prod with verbose disabled flag is rejected",
			topo:       prodTopo,
			mutate:     func(d *SharedDeps) { d.VerboseDisabled = true },
			wantErr:    true,
			wantSubstr: "GOCELL_READYZ_VERBOSE_DISABLED=1 is not allowed",
		},
		{name: "prod missing metrics token", topo: prodTopo, mutate: func(d *SharedDeps) { d.MetricsToken = "" }, wantErr: true, wantSubstr: "GOCELL_METRICS_TOKEN"},
		{name: "prod missing internal guard", topo: prodTopo, mutate: func(d *SharedDeps) { d.InternalGuard = nil }, wantErr: true, wantSubstr: "GOCELL_SERVICE_SECRET"},
		{
			name: "prod guard with noop nonce store rejected",
			topo: prodTopo,
			mutate: func(d *SharedDeps) {
				// Simulate a guard constructed without replay defense — the
				// exact condition SharedDeps.Validate must reject in prod.
				noopRing, _ := auth.NewHMACKeyRing([]byte("test-secret-32-bytes-long-padding!"), nil)
				d.InternalGuard = &internalGuard{
					ring:       noopRing,
					nonceStore: auth.NewNoopNonceStore(),
					mw:         func(h http.Handler) http.Handler { return h },
				}
			},
			wantErr:    true,
			wantSubstr: "NoopNonceStore detected",
		},
		{
			// F1: in-memory store in real mode without GOCELL_SINGLE_POD=1 must be rejected.
			name: "real mode + in_memory + single_pod=false → error",
			topo: bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real", SinglePodReplayProtection: false},
			mutate: func(d *SharedDeps) {
				// guard already has InMemoryNonceStore from newTestInternalGuard;
				// topology lacks SinglePodReplayProtection so Validate rejects.
			},
			wantErr:    true,
			wantSubstr: "GOCELL_SINGLE_POD=1",
		},
		{
			// F1: in-memory store in real mode with GOCELL_SINGLE_POD=1 is accepted.
			name:    "real mode + in_memory + single_pod=true → ok",
			topo:    bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real", SinglePodReplayProtection: true},
			mutate:  func(*SharedDeps) {},
			wantErr: false,
		},
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
			// Every joined error must be an *errcode.Error so callers can
			// classify startup failures uniformly. The specific code varies:
			// core-field and token checks use ErrValidationFailed; the
			// control-plane guard gate uses dedicated codes so operators
			// can grep service-secret and nonce-store misconfigurations
			// independently.
			allowedCodes := map[errcode.Code]struct{}{
				errcode.ErrValidationFailed:                 {},
				errcode.ErrControlplaneServiceSecretMissing: {},
				errcode.ErrControlplaneNonceStoreMissing:    {},
				errcode.ErrControlplaneVerboseTokenMissing:  {},
			}
			for _, sub := range allJoinedErrors(err) {
				var ec *errcode.Error
				require.ErrorAs(t, sub, &ec, "joined error %v must be *errcode.Error", sub)
				_, ok := allowedCodes[ec.Code]
				assert.True(t, ok, "unexpected error code %q from Validate", ec.Code)
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
	healthLn := newCorebundleLocalListener(t)

	app, err := buildBootstrapFromShared(t, shared, ln,
		bootstrap.WithListener(cell.InternalListener, "127.0.0.1:0", cell.Policy{}, bootstrap.WithListenerNet(newCorebundleLocalListener(t))),
		bootstrap.WithListener(cell.HealthListener, healthLn.Addr().String(), cell.Policy{}, bootstrap.WithListenerNet(healthLn)))
	require.NoError(t, err)
	require.NotNil(t, app)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	healthAddr := healthLn.Addr().String()
	waitForHealthy(t, healthAddr)

	// /readyz must be healthy (no PG checker to fail).
	resp, err := http.Get("http://" + healthAddr + "/readyz") //nolint:noctx
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
	healthLn := newCorebundleLocalListener(t)

	app, err := buildBootstrapFromShared(t, shared, ln,
		bootstrap.WithListener(cell.InternalListener, "127.0.0.1:0", cell.Policy{}, bootstrap.WithListenerNet(newCorebundleLocalListener(t))),
		bootstrap.WithListener(cell.HealthListener, healthLn.Addr().String(), cell.Policy{}, bootstrap.WithListenerNet(healthLn)),
		bootstrap.WithManagedResource(fakePG),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	healthAddr := healthLn.Addr().String()
	waitForHealthy(t, healthAddr)

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
// configcore, accesscore, and auditcore. We check via health + /readyz
// which would fail if any cell fails to init.
func TestBuildBootstrap_AssemblyHasAllCells(t *testing.T) {
	shared := buildTestSharedDeps(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	healthLn := newCorebundleLocalListener(t)

	app, err := buildBootstrapFromShared(t, shared, ln,
		bootstrap.WithListener(cell.InternalListener, "127.0.0.1:0", cell.Policy{}, bootstrap.WithListenerNet(newCorebundleLocalListener(t))),
		bootstrap.WithListener(cell.HealthListener, healthLn.Addr().String(), cell.Policy{}, bootstrap.WithListenerNet(healthLn)))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	healthAddr := healthLn.Addr().String()
	waitForHealthy(t, healthAddr)

	// /readyz confirms all three cells started and registered their probes.
	resp, err := http.Get("http://" + healthAddr + "/readyz") //nolint:noctx
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"all three cells (configcore, accesscore, auditcore) must be healthy")

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("full assembly bootstrap did not shut down in time")
	}
}
