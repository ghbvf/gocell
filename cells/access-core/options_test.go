package accesscore

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/outbox"
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
	deps := cell.Dependencies{Config: make(map[string]any)}
	require.NoError(t, c.Init(ctx, deps))

	r := &celltest.StubEventRouter{}
	require.NoError(t, c.RegisterSubscriptions(r))
	assert.Equal(t, 1, r.HandlerCount(), "access-core should register 1 topic handler")
	assert.Equal(t, "event.config.changed.v1", r.Topics[0])
	assert.Equal(t, "access-core", r.ConsumerGroups[0])
}

func TestInit_MissingOutboxWriter(t *testing.T) {
	// L2 cell without outboxWriter (but with txRunner) should fail via XOR check.
	c := NewAccessCore(
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithTxManager(noopTxRunner{}),
	)
	deps := cell.Dependencies{Config: make(map[string]any)}
	err := c.Init(context.Background(), deps)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "txRunner")
}

func TestInit_MissingJWTIssuerAndVerifier(t *testing.T) {
	c := NewAccessCore(
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(noopTxRunner{}),
	)
	deps := cell.Dependencies{Config: make(map[string]any)}
	err := c.Init(context.Background(), deps)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTIssuer")
	assert.Contains(t, err.Error(), "WithJWTVerifier")
}
