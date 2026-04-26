package accesscore

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithLogger(t *testing.T) {
	logger := slog.Default()
	c := NewAccessCore(WithLogger(logger))
	assert.Equal(t, logger, c.logger)
}

func TestWithInMemoryDefaults(t *testing.T) {
	c := NewAccessCore(WithInMemoryDefaults())
	assert.NotNil(t, c.userRepo)
	assert.NotNil(t, c.sessionRepo)
	assert.NotNil(t, c.roleRepo)
}

func TestHealthCheckers_InMemory(t *testing.T) {
	c := NewAccessCore(WithInMemoryDefaults())
	checkers := c.HealthCheckers()
	require.Contains(t, checkers, "session-store", "in-memory session repo implements Health()")
	assert.NoError(t, checkers["session-store"](context.Background()))
}

func TestHealthCheckers_NilRepo(t *testing.T) {
	c := NewAccessCore() // no repo set
	checkers := c.HealthCheckers()
	assert.Empty(t, checkers, "nil session repo produces no health checkers")
}

func TestRegisterSubscriptions(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo}
	require.NoError(t, c.Init(ctx, deps))

	r := &celltest.StubEventRouter{}
	require.NoError(t, c.RegisterSubscriptions(r))
	// accesscore now registers 4 topic handlers:
	//   1. event.config.entry-upserted.v1  (config-receive, consumer group: accesscore)
	//   2. event.config.entry-deleted.v1   (config-receive, consumer group: accesscore)
	//   3. event.role.assigned.v1          (rbac-session-sync, consumer group: accesscore-rbac-session-sync)
	//   4. event.role.revoked.v1           (rbac-session-sync, consumer group: accesscore-rbac-session-sync)
	assert.Equal(t, 4, r.HandlerCount(), "accesscore should register 4 topic handlers")
	assert.Equal(t, "event.config.entry-upserted.v1", r.Topics[0])
	assert.Equal(t, "accesscore", r.ConsumerGroups[0])
	assert.Equal(t, "event.config.entry-deleted.v1", r.Topics[1])
	assert.Equal(t, "accesscore", r.ConsumerGroups[1])
	assert.Equal(t, "event.role.assigned.v1", r.Topics[2])
	assert.Equal(t, "accesscore-rbac-session-sync", r.ConsumerGroups[2])
	assert.Equal(t, "event.role.revoked.v1", r.Topics[3])
	assert.Equal(t, "accesscore-rbac-session-sync", r.ConsumerGroups[3])
}

func TestInit_DurableMode_MissingOutboxWriter(t *testing.T) {
	// durableTxRunner is a non-Noop runner so the durable-mode CheckNotNoop
	// passes and we reach the actual missing-outboxWriter assertion.
	c := NewAccessCore(
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithTxManager(durableTxRunner{}),
	)
	deps := cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDurable}
	err := c.Init(context.Background(), deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outboxWriter")
}

func TestInit_DurableMode_RejectsNoopWriter(t *testing.T) {
	c := NewAccessCore(
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDurable,
	}
	err := c.Init(context.Background(), deps)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, err.Error(), "durable mode")
}

func TestInit_MissingJWTIssuerAndVerifier(t *testing.T) {
	c := NewAccessCore(
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)
	deps := cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo}
	err := c.Init(context.Background(), deps)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTIssuer")
	assert.Contains(t, err.Error(), "WithJWTVerifier")
}

// TestHealthCheckers_WithDirectEmitter verifies that after Init with a
// DirectEmitter-backed publisher, HealthCheckers returns both the
// session-store checker and the outbox-failopen-rate checker.
func TestHealthCheckers_WithDirectEmitter(t *testing.T) {
	c := NewAccessCore(
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(eventbus.New(), nil),
		WithRefreshMetricsProvider(metrics.NopProvider{}),
	)
	deps := cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo}
	require.NoError(t, c.Init(context.Background(), deps))

	checkers := c.HealthCheckers()
	require.Contains(t, checkers, "session-store", "session-store checker must be present")
	const emitterKey = "outbox-failopen-rate:accesscore"
	require.Contains(t, checkers, emitterKey, "DirectEmitter health checker must be aggregated")
	assert.NoError(t, checkers[emitterKey](context.Background()), "fresh emitter should be healthy")
}

// TestHealthCheckers_WithNoopEmitter verifies that when the emitter does not
// implement cell.HealthContributor (e.g. DiscardPublisher-backed emitter
// after Init in demo mode with no metrics), only cell-owned checkers appear.
func TestHealthCheckers_NoEmitterChecker(t *testing.T) {
	// WithEmitter(nil) → emitter field stays nil → no HealthContributor
	c := NewAccessCore(WithInMemoryDefaults())
	// Do NOT Init — emitter is nil pre-Init; HealthCheckers must handle that.
	checkers := c.HealthCheckers()
	assert.Contains(t, checkers, "session-store", "session-store must still be present")
	for k := range checkers {
		assert.NotContains(t, k, "outbox-failopen-rate",
			"nil emitter must not produce outbox checker: key=%s", k)
	}
}
