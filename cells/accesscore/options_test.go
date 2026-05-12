package accesscore

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

func TestWithLogger(t *testing.T) {
	logger := slog.Default()
	c := NewAccessCore(WithClock(clock.Real()), WithLogger(logger), withTestCASProtocol())
	assert.Equal(t, logger, c.logger)
}

// stubSetupLock is a minimal ports.SetupLock used to verify WithSetupLock wiring.
type stubSetupLock struct{}

func (stubSetupLock) Acquire(_ context.Context) error { return nil }

func TestWithSetupLock(t *testing.T) {
	lock := stubSetupLock{}
	c := NewAccessCore(WithClock(clock.Real()), WithSetupLock(lock), withTestCASProtocol())
	assert.Equal(t, lock, c.setupLock)
}

// TestWithSetupLock_NilNoop verifies that passing nil keeps the cell's setupLock
// unset (mem-mode contract: intra-process sync.Mutex in adminprovision.Provisioner
// is sufficient when no cross-process lock is wired).
func TestWithSetupLock_NilNoop(t *testing.T) {
	c := NewAccessCore(WithClock(clock.Real()), WithSetupLock(nil), withTestCASProtocol())
	assert.Nil(t, c.setupLock)
}

func TestWithInMemoryDefaults(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestBootstrapAuth(),
	)
	// userRepo, roleRepo, and sessionStore are all set eagerly via explicit options.
	assert.NotNil(t, c.userRepo)
	assert.NotNil(t, c.roleRepo)
	// Verify sessionStore is wired before Init (explicit injection, no clock deferral).
	assert.NotNil(t, c.sessionStore)
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)))
	assert.NotNil(t, c.sessionStore)
}

func TestHealthCheckers_InMemory(t *testing.T) {
	// session.MemStore does not implement Health() — session_store_ready is only
	// registered when the injected store implements the optional HealthCheckable
	// interface (reserved for infrastructure stores like the PG adapter).
	// In-memory mode deliberately has no external dependency to probe.
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestBootstrapAuth(),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	snap := rec.Snapshot()
	// MemStore does not implement HealthCheckable; no probe expected.
	assert.NotContains(t, snap.HealthCheckers, "session_store_ready",
		"session.MemStore does not implement Health(); probe must not be registered")
}

func TestHealthCheckers_WithInMemoryDefaults_SessionStorePresent(t *testing.T) {
	// session.MemStore does not implement Health() — no session_store_ready probe
	// is registered in pure in-memory mode. Init must succeed without it.
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestBootstrapAuth(),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	snap := rec.Snapshot()
	// MemStore does not implement HealthCheckable; probe absent is correct.
	assert.NotContains(t, snap.HealthCheckers, "session_store_ready",
		"session.MemStore does not implement Health(); probe must not be registered")
}

func TestRegisterSubscriptions(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
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
		WithClock(clock.Real()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDurable))
	require.Error(t, err)
	var ecErrOutbox *errcode.Error
	require.True(t, errors.As(err, &ecErrOutbox))
	assert.Contains(t, ecErrOutbox.Message+" "+ecErrOutbox.InternalMessage, "outboxWriter")
}

func TestInit_DurableMode_RejectsNoopWriter(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDurable))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, ecErr.Message+" "+ecErr.InternalMessage, "durable mode")
}

func TestInit_MissingJWTIssuerAndVerifier(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTIssuer")
	assert.Contains(t, err.Error(), "WithJWTVerifier")
}

// TestHealthCheckers_WithDirectEmitter verifies that after Init with a
// DirectEmitter-backed publisher, HealthCheckers returns both the
// session_store_ready checker and the outbox-failopen-rate checker.
func TestHealthCheckers_WithDirectEmitter(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithMetricsProvider(metrics.NopProvider{}),
		withTestCASProtocol(),
		withTestBootstrapAuth(),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))

	snap := rec.Snapshot()
	// session.MemStore does not implement HealthCheckable; no session_store_ready probe expected.
	assert.NotContains(t, snap.HealthCheckers, "session_store_ready",
		"session.MemStore does not implement Health(); probe must not be registered")
	const emitterKey = "outbox-failopen-rate.accesscore"
	require.Contains(t, snap.HealthCheckers, emitterKey, "DirectEmitter health checker must be aggregated")
	assert.NoError(t, snap.HealthCheckers[emitterKey](context.Background()), "fresh emitter should be healthy")
}

// TestHealthCheckers_WithNoopEmitter verifies that when the emitter does not
// implement emitterHealthChecker (WriterEmitter via NoopWriter path),
// only cell-owned checkers appear.
func TestHealthCheckers_NoEmitterChecker(t *testing.T) {
	// WriterEmitter (NoopWriter path) does not implement emitterHealthChecker,
	// so no outbox-failopen-rate checker is produced.
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField("password_version"))),
		withTestBootstrapAuth(),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	snap := rec.Snapshot()
	// session.MemStore does not implement HealthCheckable; no session_store_ready probe expected.
	assert.NotContains(t, snap.HealthCheckers, "session_store_ready",
		"session.MemStore does not implement Health(); probe must not be registered")
	for k := range snap.HealthCheckers {
		assert.NotContains(t, k, "outbox-failopen-rate",
			"nil emitter must not produce outbox checker: key=%s", k)
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
		WithClock(clock.Real()),
		WithUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestBootstrapAuth(),
		// withTestCASProtocol() omitted on purpose.
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
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
		WithClock(clock.Real()),
		WithUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestBootstrapAuth(),
		WithCASProtocol(nil), // bare-nil intentionally
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
}
