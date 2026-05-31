package main

import (
	"context"
	"net/http"
	"testing"

	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/outbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/metadata"
	kworker "github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/errutil"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/keystest"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/capability"
	"github.com/ghbvf/gocell/runtime/composition"
	"github.com/ghbvf/gocell/runtime/eventbus"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// newTestInternalGuard constructs an internalGuard backed by an
// InMemoryNonceStore so prod-topology validation paths see a
// replay-safe store (NonceStoreKindInMemory) rather than a Noop.
func newTestInternalGuard(t *testing.T) *internalGuard {
	t.Helper()
	ring, err := auth.NewHMACKeyRing([]byte("test-secret-32-bytes-long-padding!"), nil)
	require.NoError(t, err)
	store, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)
	return &internalGuard{
		ring:       ring,
		nonceStore: store,
		mw:         func(h http.Handler) http.Handler { return h },
	}
}

// promStackToLocals creates a minimal *cmdLocals from a promStack for tests
// that call buildAssembly / runtimeBaseOptions / defaultRuntimeOptions
// directly without going through the full runCorebundle path.
func promStackToLocals(ps promStack) *cmdLocals {
	l := &cmdLocals{
		registry:       ps.registry,
		hookObserver:   ps.hookObserver,
		metricProvider: ps.metricProvider,
	}
	l.initVaultMetricsFactory()
	return l
}

// buildTestSharedDepsAndLocals returns a minimal composition.SharedDeps and
// cmdLocals for memory topology tests.
func buildTestSharedDepsAndLocals(t *testing.T) (*composition.SharedDeps, *cmdLocals) {
	t.Helper()
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	t.Setenv("GOCELL_JWT_ISSUER", "test-issuer")
	t.Setenv("GOCELL_JWT_AUDIENCE", "test-audience")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "testadmin")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "testpassword123")

	eb := eventbus.New(clock.Real())

	privKey, pubKey := keystest.MustGenerateKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(keySet, "test-issuer", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"test-audience"}))
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("test-audience"))
	require.NoError(t, err)

	ps, err := buildPromStack()
	require.NoError(t, err)
	configEventCollector, err := obmetrics.NewProviderConfigEventCollector(ps.metricProvider)
	require.NoError(t, err)
	eventbusCacheCollector, err := obmetrics.NewProviderEventbusCacheCollector(ps.metricProvider)
	require.NoError(t, err)

	guard := newTestInternalGuard(t)

	shared, err := composition.NewSharedDeps(composition.SharedDeps{
		Clock:                  clock.Real(),
		Topology:               mkTopo("", "memory", false),
		JWTIssuer:              issuer,
		JWTVerifier:            verifier,
		MetricsProvider:        ps.metricProvider,
		EventBus:               eb,
		ConfigEventCollector:   configEventCollector,
		EventbusCacheCollector: eventbusCacheCollector,
		ConsumerClaimer:        idempotency.NewInMemClaimer(clock.Real()),
		InternalHMACRing:       guard.ring,
		InternalHTTPAddr:       "127.0.0.1:9090",
		HealthHTTPAddr:         "127.0.0.1:9091",
		// PR-A35: verbose endpoint is gated in every mode. Memory/dev tests
		// just waive it — nothing here exercises the verbose body.
		VerboseDisabled: true,
	})
	require.NoError(t, err)

	locals := &cmdLocals{
		registry:            ps.registry,
		hookObserver:        ps.hookObserver,
		metricProvider:      ps.metricProvider,
		internalGuard:       guard,
		consumerClaimerKind: consumerClaimerKindInMemory,
	}
	locals.initVaultMetricsFactory()

	return shared, locals
}

// newValidatedSharedDepsAndLocals returns composition.SharedDeps + cmdLocals
// that pass validateCorebundleDeps for the given topology. Test cases can
// mutate individual fields to assert that a specific missing field surfaces the
// expected error.
func newValidatedSharedDepsAndLocals(t *testing.T, topo bootstrap.Topology) (*composition.SharedDeps, *cmdLocals) {
	t.Helper()
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())

	privKey, pubKey := keystest.MustGenerateKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(keySet, "test-issuer", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"test-audience"}))
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("test-audience"))
	require.NoError(t, err)

	ps, err := buildPromStack()
	require.NoError(t, err)
	configEventCollector, err := obmetrics.NewProviderConfigEventCollector(ps.metricProvider)
	require.NoError(t, err)
	eventbusCacheCollector, err := obmetrics.NewProviderEventbusCacheCollector(ps.metricProvider)
	require.NoError(t, err)

	guard := newTestInternalGuard(t)

	shared := &composition.SharedDeps{
		Clock:                  clock.Real(),
		Topology:               topo,
		JWTIssuer:              issuer,
		JWTVerifier:            verifier,
		MetricsProvider:        ps.metricProvider,
		EventBus:               eventbus.New(clock.Real()),
		ConfigEventCollector:   configEventCollector,
		EventbusCacheCollector: eventbusCacheCollector,
		ConsumerClaimer:        idempotency.NewInMemClaimer(clock.Real()),
		InternalHMACRing:       guard.ring,
		InternalHTTPAddr:       "127.0.0.1:9090",
		HealthHTTPAddr:         ":9091",
		// PR-A35: verbose endpoint is now gated in every mode. A test-time
		// token keeps the dev baseline valid; prod tests override via the
		// mutate callback when they want to exercise the missing-token path.
		VerboseToken: "test-verbose",
	}
	if topo.RequireProductionControlPlane() {
		shared.MetricsToken = "test-metrics"
	}

	locals := &cmdLocals{
		registry:            ps.registry,
		hookObserver:        ps.hookObserver,
		metricProvider:      ps.metricProvider,
		internalGuard:       guard,
		consumerClaimerKind: consumerClaimerKindInMemory,
	}
	locals.initVaultMetricsFactory()

	return shared, locals
}

// fakeManagedResource implements lifecycle.ManagedResource for tests.
type fakeManagedResource struct {
	closeCalled bool
	w           kworker.Worker
}

func (f *fakeManagedResource) Probes() []healthz.Probe {
	return []healthz.Probe{
		healthz.NewProbe(healthz.MustProbeName("fake_resource_ready"), func(context.Context) error { return nil }),
	}
}

func (f *fakeManagedResource) Worker() kworker.Worker { return f.w }

func (f *fakeManagedResource) Close(_ context.Context) error {
	f.closeCalled = true
	return nil
}

var _ kernellifecycle.ManagedResource = (*fakeManagedResource)(nil)

// ---------------------------------------------------------------------------
// buildInternalAuthChain coverage
// ---------------------------------------------------------------------------

// TestBuildInternalAuthChain_NonNilGuard_ReturnsServiceToken verifies that
// a guard produces an AuthServiceToken plan in the chain.
func TestBuildInternalAuthChain_NonNilGuard_ReturnsServiceToken(t *testing.T) {
	guard := newTestInternalGuard(t)
	chain, err := buildInternalAuthChain(guard)
	require.NoError(t, err)
	require.Len(t, chain, 1, "guard must produce a 1-plan chain")
	_, ok := chain[0].(kauth.AuthServiceToken)
	assert.True(t, ok, "plan must be kauth.AuthServiceToken; got %T", chain[0])
}

// TestBuildInternalAuthChain_NoopNonceStoreRejected verifies that
// buildInternalAuthChain returns an error when the guard's NonceStore has
// Kind() == NonceStoreKindNoop. kauth.NewAuthServiceToken enforces replay
// protection is not silently disabled.
func TestBuildInternalAuthChain_NoopNonceStoreRejected(t *testing.T) {
	ring, err := auth.NewHMACKeyRing([]byte("test-secret-32-bytes-long-padding!"), nil)
	require.NoError(t, err)
	guardWithNoop := &internalGuard{
		ring:       ring,
		nonceStore: auth.NewNoopNonceStore(),
		mw:         func(h http.Handler) http.Handler { return h },
	}

	_, err = buildInternalAuthChain(guardWithNoop)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "build internal auth chain",
		"error must be wrapped with build-site context")
}

// ---------------------------------------------------------------------------
// buildAssembly coverage
// ---------------------------------------------------------------------------

// TestBuildAssembly_RegisterError verifies that buildAssembly propagates the
// error returned by asm.Register when a duplicate cell ID is detected.
func TestBuildAssembly_RegisterError(t *testing.T) {
	ps, err := buildPromStack()
	require.NoError(t, err)

	// Two cells with the same ID causes asm.Register to fail on the second call.
	c1 := cell.MustNewBaseCell(&metadata.CellMeta{ID: "dup-cell", Type: "core"})
	c2 := cell.MustNewBaseCell(&metadata.CellMeta{ID: "dup-cell", Type: "core"})

	_, err = buildAssembly(promStackToLocals(ps), "corebundle", outbox.DurabilityDemo, clock.Real(), c1, c2)
	require.Error(t, err, "duplicate cell ID must cause buildAssembly to return an error")
	assert.Contains(t, err.Error(), "dup-cell",
		"error must mention the duplicate cell ID so operators can diagnose the conflict")
}

// ---------------------------------------------------------------------------
// buildConsumerBase coverage
// ---------------------------------------------------------------------------

// TestBuildConsumerBase_ReturnsNonNil verifies the happy path of
// buildConsumerBase: the returned ConsumerBase must be non-nil and usable.
func TestBuildConsumerBase_ReturnsNonNil(t *testing.T) {
	shared, _ := buildTestSharedDepsAndLocals(t)
	cb, err := buildConsumerBase(shared)
	require.NoError(t, err)
	require.NotNil(t, cb, "buildConsumerBase must return a non-nil ConsumerBase")
}

func TestBuildConsumerBase_RealMultiPodMissingDistributedClaimerErrors(t *testing.T) {
	shared, _ := newValidatedSharedDepsAndLocals(t, mkTopo("real", "postgres", false))
	shared.ConsumerClaimer = nil

	cb, err := buildConsumerBase(shared)

	require.Error(t, err)
	assert.Nil(t, cb)
	assert.Contains(t, err.Error(), "ConsumerClaimer")
}

func TestBuildConsumerBase_NilSharedDepsErrors(t *testing.T) {
	cb, err := buildConsumerBase(nil)

	require.Error(t, err)
	assert.Nil(t, cb)
	assert.Contains(t, err.Error(), "SharedDeps is nil")
}

// ---------------------------------------------------------------------------
// defaultRuntimeOptions / runtimeBaseOptions coverage
// ---------------------------------------------------------------------------

func TestDefaultRuntimeOptions_IncludesRedisHealthAndCloser(t *testing.T) {
	shared, locals := buildTestSharedDepsAndLocals(t)
	shared.InternalHTTPAddr = "127.0.0.1:0"
	locals.internalGuard = newTestInternalGuard(t)
	shared.InternalHMACRing = locals.internalGuard.ring
	asm := assembly.New(clock.Real(), assembly.Config{ID: "test-redis-options", DurabilityMode: outbox.DurabilityDemo})
	cb, err := buildConsumerBase(shared)
	require.NoError(t, err)

	base, err := defaultRuntimeOptions(shared, locals, asm, cb, http.NewServeMux(), adapterInfoForSharedDeps(shared, locals))
	require.NoError(t, err)
	shared.Redis = capability.NewRedisProvider(new(adapterredis.Client))
	withRedis, err := defaultRuntimeOptions(shared, locals, asm, cb, http.NewServeMux(), adapterInfoForSharedDeps(shared, locals))
	require.NoError(t, err)

	// PR-8 OIDC-MR-COMPLETENESS Group C: WithHealthChecker+WithManagedCloser collapsed
	// into a single WithManagedResource, so redis adds exactly 1 option (not 2).
	assert.Len(t, withRedis, len(base)+1)
}

// TestDefaultRuntimeOptions_PrimaryAuthErrOnNilAssembly verifies the error path
// in defaultRuntimeOptions when a non-empty PrimaryHTTPAddr is set and asm is
// nil. kauth.NewAuthJWTFromAssembly rejects nil interfaces, so the function must
// return an error containing "primary listener auth".
func TestDefaultRuntimeOptions_PrimaryAuthErrOnNilAssembly(t *testing.T) {
	shared, locals := buildTestSharedDepsAndLocals(t)
	shared.PrimaryHTTPAddr = ":8080" // non-empty → primary listener branch executes

	cb, err := buildConsumerBase(shared)
	require.NoError(t, err)

	// Pass nil assembly — NewAuthJWTFromAssembly rejects nil via IsNilInterface.
	_, err = defaultRuntimeOptions(shared, locals, nil, cb, http.NewServeMux(), adapterInfoForSharedDeps(shared, locals))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "primary listener auth",
		"error must identify which listener auth failed for operator diagnosis")
}

// ---------------------------------------------------------------------------
// adapterInfoForSharedDeps coverage
// ---------------------------------------------------------------------------

func TestAdapterInfoForSharedDeps_IncludesReplayState(t *testing.T) {
	shared, locals := newValidatedSharedDepsAndLocals(t, mkTopo("real", "postgres", true))

	info := adapterInfoForSharedDeps(shared, locals)

	assert.Equal(t, "not-configured", info["redis"])
	assert.Equal(t, string(kauth.NonceStoreKindInMemory), info["service_token_nonce_store"])
	assert.Equal(t, string(consumerClaimerKindInMemory), info["outbox_consumer_claimer"])

	locals.redisClient = new(adapterredis.Client)
	locals.consumerClaimerKind = consumerClaimerKindDistributed

	info = adapterInfoForSharedDeps(shared, locals)

	assert.Equal(t, "configured", info["redis"])
	assert.Equal(t, string(consumerClaimerKindDistributed), info["outbox_consumer_claimer"])
}

// ---------------------------------------------------------------------------
// validateCorebundleDeps / SharedDeps.Validate coverage
// ---------------------------------------------------------------------------

// TestCorebundleDepsValidate covers every invariant enforced by validateCorebundleDeps.
// Each case takes a baseline that passes and mutates one field to verify the
// validation surfaces that specific failure with the expected error.
func TestCorebundleDepsValidate(t *testing.T) {
	// SinglePodReplayProtection=true acknowledges in-memory replay defense scope
	// for single-pod deployments (mirrors GOCELL_SINGLE_POD=1).
	prodTopo := mkTopo("real", "postgres", true)
	devTopo := mkTopo("", "memory", false)

	cases := []struct {
		name         string
		topo         bootstrap.Topology
		mutateShared func(*composition.SharedDeps)
		mutateLocals func(*cmdLocals)
		wantErr      bool
		wantSubstr   string
	}{
		{
			name: "prod baseline is valid", topo: prodTopo,
			mutateShared: func(*composition.SharedDeps) {}, mutateLocals: func(*cmdLocals) {}, wantErr: false,
		},
		{
			name: "dev baseline is valid", topo: devTopo,
			mutateShared: func(*composition.SharedDeps) {}, mutateLocals: func(*cmdLocals) {}, wantErr: false,
		},
		{
			name:         "prod missing verbose token",
			topo:         prodTopo,
			mutateShared: func(d *composition.SharedDeps) { d.VerboseToken = "" },
			mutateLocals: func(*cmdLocals) {},
			wantErr:      true, wantSubstr: "GOCELL_READYZ_VERBOSE_TOKEN",
		},
		{
			name:         "dev missing verbose token",
			topo:         devTopo,
			mutateShared: func(d *composition.SharedDeps) { d.VerboseToken = "" },
			mutateLocals: func(*cmdLocals) {},
			wantErr:      true, wantSubstr: "GOCELL_READYZ_VERBOSE_TOKEN",
		},
		{
			name:         "dev with verbose disabled flag is valid",
			topo:         devTopo,
			mutateShared: func(d *composition.SharedDeps) { d.VerboseToken = ""; d.VerboseDisabled = true },
			mutateLocals: func(*cmdLocals) {},
			wantErr:      false,
		},
		{
			name:         "prod with verbose disabled flag is rejected",
			topo:         prodTopo,
			mutateShared: func(d *composition.SharedDeps) { d.VerboseDisabled = true },
			mutateLocals: func(*cmdLocals) {},
			wantErr:      true, wantSubstr: "GOCELL_READYZ_VERBOSE_DISABLED=1 is not allowed",
		},
		{
			name:         "prod missing metrics token",
			topo:         prodTopo,
			mutateShared: func(d *composition.SharedDeps) { d.MetricsToken = "" },
			mutateLocals: func(*cmdLocals) {},
			wantErr:      true, wantSubstr: "GOCELL_METRICS_TOKEN",
		},
		{
			name:         "prod missing internal guard",
			topo:         prodTopo,
			mutateShared: func(*composition.SharedDeps) {},
			mutateLocals: func(l *cmdLocals) { l.internalGuard = nil },
			wantErr:      true, wantSubstr: "GOCELL_SERVICE_SECRET",
		},
		{
			name:         "real multi-pod with in-memory claimer rejected",
			topo:         mkTopo("real", "postgres", false),
			mutateShared: func(*composition.SharedDeps) {},
			mutateLocals: func(l *cmdLocals) { l.consumerClaimerKind = consumerClaimerKindInMemory },
			wantErr:      true, wantSubstr: "ERR_CONTROLPLANE_CLAIMER_NOT_DISTRIBUTED",
		},
		{
			name:         "prod guard with noop nonce store rejected",
			topo:         prodTopo,
			mutateShared: func(*composition.SharedDeps) {},
			mutateLocals: func(l *cmdLocals) {
				noopRing, _ := auth.NewHMACKeyRing([]byte("test-secret-32-bytes-long-padding!"), nil)
				l.internalGuard = &internalGuard{
					ring:       noopRing,
					nonceStore: auth.NewNoopNonceStore(),
					mw:         func(h http.Handler) http.Handler { return h },
				}
			},
			wantErr: true, wantSubstr: "NoopNonceStore detected",
		},
		{
			name:         "real mode + in_memory + single_pod=false → error",
			topo:         mkTopo("real", "postgres", false),
			mutateShared: func(*composition.SharedDeps) {},
			mutateLocals: func(*cmdLocals) {
				// guard already has InMemoryNonceStore from newTestInternalGuard;
				// topology lacks SinglePodReplayProtection so Validate rejects.
			},
			wantErr: true, wantSubstr: "GOCELL_SINGLE_POD=1",
		},
		{
			name:         "real mode + in_memory + single_pod=true → ok",
			topo:         mkTopo("real", "postgres", true),
			mutateShared: func(*composition.SharedDeps) {},
			mutateLocals: func(*cmdLocals) {},
			wantErr:      false,
		},
		{
			name:         "prod rejects the .env.example sample verbose token",
			topo:         prodTopo,
			mutateShared: func(d *composition.SharedDeps) { d.VerboseToken = SampleVerbosePlaceholder },
			mutateLocals: func(*cmdLocals) {},
			wantErr:      true, wantSubstr: "ERR_CONTROLPLANE_VERBOSE_TOKEN_SAMPLE",
		},
		{
			name:         "dev permits the sample verbose token (out-of-the-box demo path)",
			topo:         devTopo,
			mutateShared: func(d *composition.SharedDeps) { d.VerboseToken = SampleVerbosePlaceholder },
			mutateLocals: func(*cmdLocals) {},
			wantErr:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shared, locals := newValidatedSharedDepsAndLocals(t, tc.topo)
			tc.mutateShared(shared)
			tc.mutateLocals(locals)

			err := validateCorebundleDeps(shared, locals)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSubstr, "error must mention the offending field")
			// Every joined error must be an *errcode.Error so callers can
			// classify startup failures uniformly.
			allowedCodes := map[errcode.Code]struct{}{
				errcode.ErrValidationFailed:                  {},
				errcode.ErrControlplaneServiceSecretMissing:  {},
				errcode.ErrControlplaneNonceStoreMissing:     {},
				errcode.ErrControlplaneVerboseTokenMissing:   {},
				errcode.ErrControlplaneVerboseTokenSample:    {},
				errcode.ErrControlplaneClaimerNotDistributed: {},
			}
			for _, sub := range errutil.FlattenJoined(err) {
				var ec *errcode.Error
				require.ErrorAs(t, sub, &ec, "joined error %v must be *errcode.Error", sub)
				_, ok := allowedCodes[ec.Code]
				assert.True(t, ok, "unexpected error code %q from validateCorebundleDeps", ec.Code)
			}
		})
	}
}

// TestCorebundleDepsValidate_NilSharedDeps covers the defensive nil-shared case.
func TestCorebundleDepsValidate_NilSharedDeps(t *testing.T) {
	ps, err := buildPromStack()
	require.NoError(t, err)
	locals := promStackToLocals(ps)
	err = validateCorebundleDeps(nil, locals)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil receiver")
}

// ---------------------------------------------------------------------------
// devtoolsOption coverage
// ---------------------------------------------------------------------------

// TestDevtoolsOption_EmptyRoot verifies that devtoolsOption returns a no-op
// bootstrap option (catalog endpoint disabled) when ProjectRoot is unset.
func TestDevtoolsOption_EmptyRoot(t *testing.T) {
	shared, _ := buildTestSharedDepsAndLocals(t)
	shared.ProjectRoot = "" // unset → catalog disabled

	opt := devtoolsOption(shared)
	require.NotNil(t, opt, "devtoolsOption must always return a non-nil Option")

	// Apply option to a bootstrap and verify devtoolsMeta is nil (disabled).
	b := bootstrap.New(shared.Clock, opt)
	require.NotNil(t, b)
}

// TestDevtoolsOption_RootOutsideCwd verifies that devtoolsOption disables the
// catalog when ProjectRoot resolves outside the current working directory.
func TestDevtoolsOption_RootOutsideCwd(t *testing.T) {
	shared, _ := buildTestSharedDepsAndLocals(t)
	// /tmp is virtually never within the test's cwd.
	shared.ProjectRoot = t.TempDir()

	opt := devtoolsOption(shared)
	require.NotNil(t, opt)

	b := bootstrap.New(shared.Clock, opt)
	require.NotNil(t, b)
}

// ---------------------------------------------------------------------------
// durabilityModeForTopology coverage
// ---------------------------------------------------------------------------

func TestDurabilityModeForTopology_UsesStorageBackend(t *testing.T) {
	tests := []struct {
		name string
		topo bootstrap.Topology
		want outbox.DurabilityMode
	}{
		{
			name: "memory real remains demo",
			topo: mkTopo("real", "memory", false),
			want: outbox.DurabilityDemo,
		},
		{
			name: "postgres real is durable",
			topo: mkTopo("real", "postgres", false),
			want: outbox.DurabilityDurable,
		},
		{
			name: "memory dev remains demo",
			topo: mkTopo("", "memory", false),
			want: outbox.DurabilityDemo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, durabilityModeForTopology(tt.topo))
		})
	}
}
