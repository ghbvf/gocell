package accesscore

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
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
	assert.NoError(t, checkers["session-store"]())
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
	// access-core now registers 3 topic handlers:
	//   1. event.config.changed.v1    (config-receive, consumer group: access-core)
	//   2. event.role.assigned.v1     (rbac-session-sync, consumer group: access-core-rbac-session-sync)
	//   3. event.role.revoked.v1      (rbac-session-sync, consumer group: access-core-rbac-session-sync)
	assert.Equal(t, 3, r.HandlerCount(), "access-core should register 3 topic handlers")
	assert.Equal(t, "event.config.changed.v1", r.Topics[0])
	assert.Equal(t, "access-core", r.ConsumerGroups[0])
	assert.Equal(t, "event.role.assigned.v1", r.Topics[1])
	assert.Equal(t, "access-core-rbac-session-sync", r.ConsumerGroups[1])
	assert.Equal(t, "event.role.revoked.v1", r.Topics[2])
	assert.Equal(t, "access-core-rbac-session-sync", r.ConsumerGroups[2])
}

func TestInit_MissingOutboxWriter(t *testing.T) {
	// L2 cell without outboxWriter (but with txRunner) should fail via XOR check.
	c := NewAccessCore(
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithTxManager(noopTxRunner{}),
	)
	deps := cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo}
	err := c.Init(context.Background(), deps)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "txRunner")
}

func TestInit_DurableMode_RejectsNoopWriter(t *testing.T) {
	c := NewAccessCore(
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxWriter(outbox.NoopWriter{}),
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
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(noopTxRunner{}),
	)
	deps := cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo}
	err := c.Init(context.Background(), deps)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTIssuer")
	assert.Contains(t, err.Error(), "WithJWTVerifier")
}
