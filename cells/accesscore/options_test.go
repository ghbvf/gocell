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
