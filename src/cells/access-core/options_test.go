package accesscore

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
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

func TestRegisterSubscriptions(t *testing.T) {
	c := newTestCell()
	err := c.RegisterSubscriptions(nil)
	require.NoError(t, err)
}

func TestInit_MissingOutboxWriter(t *testing.T) {
	// L2 cell without outboxWriter should fail.
	c := NewAccessCore(
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
	)
	deps := cell.Dependencies{Config: make(map[string]any)}
	err := c.Init(context.Background(), deps)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "outboxWriter")
}

func TestInit_MissingJWTIssuerAndVerifier(t *testing.T) {
	c := NewAccessCore(
		WithOutboxWriter(noopWriter{}),
	)
	deps := cell.Dependencies{Config: make(map[string]any)}
	err := c.Init(context.Background(), deps)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTIssuer")
	assert.Contains(t, err.Error(), "WithJWTVerifier")
}
