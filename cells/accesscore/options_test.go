package accesscore

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

func TestWithLogger(t *testing.T) {
	logger := slog.Default()
	c := NewAccessCore(WithClock(clock.Real()), WithLogger(logger))
	assert.Equal(t, logger, c.logger)
}

// stubSetupLock is a minimal ports.SetupLock used to verify WithSetupLock wiring.
type stubSetupLock struct{}

func (stubSetupLock) Acquire(_ context.Context) error { return nil }

func TestWithSetupLock(t *testing.T) {
	lock := stubSetupLock{}
	c := NewAccessCore(WithClock(clock.Real()), WithSetupLock(lock))
	assert.Equal(t, lock, c.setupLock)
}

// TestWithSetupLock_NilNoop verifies that passing nil keeps the cell's setupLock
// unset (mem-mode contract: intra-process sync.Mutex in adminprovision.Provisioner
// is sufficient when no cross-process lock is wired).
func TestWithSetupLock_NilNoop(t *testing.T) {
	c := NewAccessCore(WithClock(clock.Real()), WithSetupLock(nil))
	assert.Nil(t, c.setupLock)
}

func TestWithInMemoryDefaults(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
		withTestBootstrapAuth(),
	)
	// userRepo and roleRepo are set eagerly; sessionRepo is deferred to Init()
	// so that c.clk is available (clock injection pattern).
	assert.NotNil(t, c.userRepo)
	assert.NotNil(t, c.roleRepo)
	// Verify sessionRepo is wired after Init.
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)))
	assert.NotNil(t, c.sessionRepo)
}

func TestHealthCheckers_InMemory(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
		withTestBootstrapAuth(),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	snap := rec.Snapshot()
	require.Contains(t, snap.HealthCheckers, "session_store_ready", "in-memory session repo implements Health()")
	assert.NoError(t, snap.HealthCheckers["session_store_ready"](context.Background()))
}

func TestHealthCheckers_WithInMemoryDefaults_SessionStorePresent(t *testing.T) {
	// WithInMemoryDefaults defers sessionRepo construction to Init() so that
	// c.clk is available; after Init the session-store health probe is registered.
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithInMemoryDefaults(),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
		withTestBootstrapAuth(),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	snap := rec.Snapshot()
	assert.Contains(t, snap.HealthCheckers, "session_store_ready")
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
		WithTxManager(durableTxRunner{}),
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
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
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
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
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
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
		withTestBootstrapAuth(),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))

	snap := rec.Snapshot()
	require.Contains(t, snap.HealthCheckers, "session_store_ready", "session_store_ready checker must be present")
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
	// sessionRepo is deferred to Init() (clock injection pattern), so Init
	// must be called before snapshot to have session-store present.
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
		withTestBootstrapAuth(),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	snap := rec.Snapshot()
	assert.Contains(t, snap.HealthCheckers, "session_store_ready", "session-store must still be present")
	for k := range snap.HealthCheckers {
		assert.NotContains(t, k, "outbox-failopen-rate",
			"nil emitter must not produce outbox checker: key=%s", k)
	}
}
