package accesscore

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
	"github.com/ghbvf/gocell/framework/runtime/state/cas"
)

func TestWithLogger(t *testing.T) {
	logger := slog.Default()
	c := NewAccessCore(clock.Real(), WithLogger(logger), withTestCASProtocol())
	assert.Equal(t, logger, c.logger)
}

// stubSetupLock is a minimal ports.SetupLockAcquirer used to verify WithSetupLock wiring.
type stubSetupLock struct{}

func (stubSetupLock) Acquire(_ context.Context) error { return nil }

func TestWithSetupLock(t *testing.T) {
	lock := stubSetupLock{}
	c := NewAccessCore(clock.Real(), withSetupLock(lock), withTestCASProtocol())
	assert.Equal(t, lock, c.setupLock)
}

// TestInit_MissingSetupLock_FailsFast verifies that omitting WithSetupLock
// from the composition root causes Init() to return ErrCellInvalidConfig at
// phase0 — closing the upstream-Soft gap that was previously plugged by the
// in-process sync.Mutex inside adminprovision.Provisioner (removed in this PR).
// Memstore composition roots wire accesscore.NoopSetupLock{}; PG composition
// roots wire accesspg.NewBundle(pool, txm, clk).SetupLock().
func TestInit_MissingSetupLock_FailsFast(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestBootstrapAuth(),
		// withTestSetupLock() omitted on purpose.
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	require.Error(t, err, "missing WithSetupLock must produce a phase0 error")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
	assert.Contains(t, ec.Message, "setupLock is required",
		"diagnostic must point operators at the missing wiring")
}

// TestWithSetupLock_NilOption_RejectedAtInit verifies that nil ports.SetupLockAcquirer
// — in any of three forms (bare nil, typed-nil interface, typed-nil concrete
// pointer) — never satisfies the WithSetupLock required-dep check. Each form
// must produce ErrCellInvalidConfig at phase0; the option body's
// validation.IsNilInterface check is the upstream funnel.
func TestWithSetupLock_NilOption_RejectedAtInit(t *testing.T) {
	var typedNilIface ports.SetupLockAcquirer // typed-nil interface
	var typedNilPtr *stubSetupLock            // typed-nil concrete pointer (still nil via IsNilInterface)
	cases := []struct {
		name string
		lock ports.SetupLockAcquirer
	}{
		{"bare nil", nil},
		{"typed nil interface", typedNilIface},
		{"typed nil pointer", typedNilPtr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewAccessCore(
				clock.Real(),
				withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
				withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
				withPolicyRepository(mem.NewPolicyRepository()),
				withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
				WithSessionStore(testutil.RealSessionRepo(t)),
				WithJWTIssuer(testIssuer),
				WithJWTVerifier(testVerifier),
				WithRefreshStore(newTestRefreshStore()),
				WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
				withTxManager(persistence.WrapForCell(durableTxRunner{})),
				withTestCASProtocol(),
				withTestBootstrapAuth(),
				withSetupLock(tc.lock),
			)
			err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
			require.Error(t, err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
		})
	}
}

func TestWithInMemoryDefaults(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	// userRepo, roleRepo, and sessionStore are all set eagerly via explicit options.
	assert.NotNil(t, c.userRepo)
	assert.NotNil(t, c.roleRepo)
	// Verify sessionStore is wired before Init (explicit injection, no clock deferral).
	assert.NotNil(t, c.sessionStore)
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)))
	assert.NotNil(t, c.sessionStore)
}

func TestHealthCheckers_InMemory(t *testing.T) {
	// session.Store now satisfies healthz.RepoProber via RepoReady. The
	// repo probe is registered for ALL store implementations (including MemStore)
	// through the cellgen-generated RegisterReadiness funnel.
	// MemStore.RepoReady returns nil — in-memory always ready.
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	agg := drainProbeSnapshot(t, rec)
	// ProbeRepoReady = "accesscore_repo_ready" (cellgen-generated constant).
	require.True(t, agg.HasProbe(ProbeRepoReady),
		"session.Store satisfies RepoProber; repo probe must be registered")
	assert.NoError(t, agg.Probe(ProbeRepoReady).Check(context.Background()),
		"MemStore.RepoReady must return nil (in-memory always ready)")
}

func TestHealthCheckers_WithInMemoryDefaults_SessionStorePresent(t *testing.T) {
	// session.Store satisfies healthz.RepoProber via RepoReady. The probe is
	// registered unconditionally (including MemStore) through RegisterReadiness.
	c := NewAccessCore(
		clock.Real(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	agg := drainProbeSnapshot(t, rec)
	// ProbeRepoReady = "accesscore_repo_ready" (cellgen-generated constant).
	require.True(t, agg.HasProbe(ProbeRepoReady),
		"session.Store satisfies RepoProber; repo probe must be registered")
	assert.NoError(t, agg.Probe(ProbeRepoReady).Check(context.Background()),
		"MemStore.RepoReady must return nil (in-memory always ready)")
}

// okProber / failingProber are minimal healthz.RepoProber stubs for the
// composite-readiness test below.
type okProber struct{}

func (okProber) RepoReady(context.Context) error { return nil }

type failingProber struct{ err error }

func (f failingProber) RepoReady(context.Context) error { return f.err }

// TestRepoReadyAll_FailClosed verifies the composite RepoProber that folds the
// session + policy stores into accesscore_repo_ready: ready only when every
// member is ready, returning the first not-ready member's error (fail-closed).
func TestRepoReadyAll_FailClosed(t *testing.T) {
	ctx := context.Background()

	// Empty + all-ready composites report ready.
	assert.NoError(t, repoReadyAll{}.RepoReady(ctx), "empty composite is vacuously ready")
	assert.NoError(t, repoReadyAll{okProber{}, okProber{}}.RepoReady(ctx), "all-ready composite is ready")

	// A single not-ready member makes the whole composite not-ready, surfacing
	// that member's error unchanged.
	wantErr := errors.New("policy store unreachable")
	err := repoReadyAll{okProber{}, failingProber{err: wantErr}}.RepoReady(ctx)
	assert.ErrorIs(t, err, wantErr, "composite must fail closed with the not-ready member's error")
}

// TestRepoReadyAll_Conformance enrolls the composite RepoProber in the
// healthz.RepoProber readiness conformance (CELL-REPO-READYZ-PROBE-01). It has a
// differentiated failure domain (any not-ready member), so a non-nil broken
// prober is supplied.
func TestRepoReadyAll_Conformance(t *testing.T) {
	celltest.RunRepoReadinessConformance(t, "accesscore-repo-all",
		repoReadyAll{okProber{}},
		repoReadyAll{failingProber{err: errors.New("member store unreachable")}})
}

func TestRegisterSubscriptions(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(ctx, rec))

	snap := rec.Snapshot()
	// accesscore registers 4 topic handlers:
	//   event.config.entry-upserted.v1  (config-receive, consumer group: accesscore)
	//   event.config.entry-deleted.v1   (config-receive, consumer group: accesscore)
	//   event.role.assigned.v1          (rbac-session-sync, consumer group: accesscore-rbac-session-sync)
	//   event.role.revoked.v1           (rbac-session-sync, consumer group: accesscore-rbac-session-sync)
	//
	// cell_gen.go sorts subscriptions alphabetically by contract ID for diff
	// stability, so positional assertions would be brittle. Use a map instead.
	require.Len(t, snap.Subscriptions, 4, "accesscore should register 4 topic handlers")
	groups := make(map[string]string, 4)
	for _, sub := range snap.Subscriptions {
		groups[sub.Spec.Topic] = sub.ConsumerGroup
	}
	// New codegen pattern: Topic == ContractID after PR-CODEGEN-FULL-MIGRATION-FU.
	assert.Equal(t, "accesscore", groups["event.config.entry-upserted.v1"])
	assert.Equal(t, "accesscore", groups["event.config.entry-deleted.v1"])
	assert.Equal(t, "accesscore-rbac-session-sync", groups["event.role.assigned.v1"])
	assert.Equal(t, "accesscore-rbac-session-sync", groups["event.role.revoked.v1"])
}

func TestInit_DurableMode_MissingOutboxWriter(t *testing.T) {
	// durableTxRunner is a non-Noop runner so the durable-mode CheckNotNoop
	// passes and we reach the actual missing-outboxWriter assertion.
	c := NewAccessCore(
		clock.Real(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDurable))
	require.Error(t, err)
	var ecErrOutbox *errcode.Error
	require.True(t, errors.As(err, &ecErrOutbox))
	assert.Contains(t, ecErrOutbox.Message+" "+ecErrOutbox.Error(), "outboxWriter")
}

func TestInit_DurableMode_RejectsNoopWriter(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDurable))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, ecErr.Message+" "+ecErr.Error(), "durable mode")
}

func TestInit_MissingJWTIssuerAndVerifier(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTIssuer")
	assert.Contains(t, err.Error(), "WithJWTVerifier")
}

// TestHealthCheckers_WithDirectEmitter verifies that after Init with a
// DirectEmitter-backed publisher, both the repo probe and the
// outbox_failopen_rate probe are registered.
func TestHealthCheckers_WithDirectEmitter(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithMetricsProvider(metrics.NopProvider{}),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	agg := drainProbeSnapshot(t, rec)

	// ProbeRepoReady = "accesscore_repo_ready" (cellgen-generated constant).
	require.True(t, agg.HasProbe(ProbeRepoReady),
		"session.Store satisfies RepoProber; repo probe must be registered")
	assert.NoError(t, agg.Probe(ProbeRepoReady).Check(context.Background()),
		"MemStore.RepoReady must return nil (in-memory always ready)")
	const emitterKey = "outbox_failopen_rate_accesscore"
	require.True(t, agg.HasProbe(emitterKey), "DirectEmitter health checker must be aggregated")
	assert.NoError(t, agg.Probe(emitterKey).Check(context.Background()), "fresh emitter should be healthy")
}

// TestHealthCheckers_WithNoopEmitter verifies that when the emitter does not
// implement healthz.ProbeSet (WriterEmitter via NoopWriter path),
// only the repo probe appears — no emitter probes.
func TestHealthCheckers_NoEmitterChecker(t *testing.T) {
	// WriterEmitter (NoopWriter path) does not implement healthz.ProbeSet,
	// so no outbox_failopen_rate probe is registered.
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithCASProtocol(func() *cas.Protocol {
			p, err := cas.NewProtocol(cas.WithVersionField("password_version"))
			require.NoError(t, err)
			return p
		}()),
		withTestBootstrapAuth(),
		withTestSetupLock(),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	agg := drainProbeSnapshot(t, rec)
	// ProbeRepoReady = "accesscore_repo_ready" (cellgen-generated constant).
	require.True(t, agg.HasProbe(ProbeRepoReady),
		"session.Store satisfies RepoProber; repo probe must be registered")
	assert.NoError(t, agg.Probe(ProbeRepoReady).Check(context.Background()),
		"MemStore.RepoReady must return nil (in-memory always ready)")
	for _, k := range agg.ProbeNames() {
		assert.NotContains(t, k, "outbox_failopen_rate",
			"WriterEmitter must not produce outbox probe: key=%s", k)
	}
}

// ---------------------------------------------------------------------------
// PR464 P2.1 follow-up: cover phase0 missing-CASProtocol rejection path so
// regression catches a composition root that forgets WithCASProtocol.
// ---------------------------------------------------------------------------

// TestInit_MissingCASProtocol_FailsFast verifies that omitting WithCASProtocol
// from the composition root causes Init() to return ErrCellInvalidConfig at
// phase0 — protecting the ChangePassword concurrent-write guard.
func TestInit_MissingCASProtocol_FailsFast(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestBootstrapAuth(),
		withTestSetupLock(),
		// withTestCASProtocol() omitted on purpose.
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	require.Error(t, err, "missing WithCASProtocol must produce a phase0 error")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
	assert.Contains(t, ec.Message, "WithCASProtocol is required",
		"diagnostic must point operators at the missing wiring")
}

// TestWithCASProtocol_NilOption_IgnoredAndCaughtAtInit verifies that a typed-nil
// *cas.Protocol passed via WithCASProtocol does NOT silently override a real
// protocol (it is ignored, leaving phase0 to reject when nothing else wired one).
func TestWithCASProtocol_NilOption_IgnoredAndCaughtAtInit(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestBootstrapAuth(),
		withTestSetupLock(),
		WithCASProtocol(nil), // bare-nil intentionally
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
}
