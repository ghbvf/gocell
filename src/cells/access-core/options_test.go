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

func TestWithSigningKey(t *testing.T) {
	key := []byte("test-key-at-least-32-bytes-long!!")
	c := NewAccessCore(WithSigningKey(key))
	assert.Equal(t, key, c.signingKey)
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

func TestInit_SigningKeyTooShort(t *testing.T) {
	c := NewAccessCore(
		WithSigningKey([]byte("short")),
		WithOutboxWriter(noopWriter{}),
	)
	deps := cell.Dependencies{Config: make(map[string]any)}
	err := c.Init(context.Background(), deps)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least 32 bytes")
}

func TestInit_MissingJWTAndSigningKey(t *testing.T) {
	c := NewAccessCore(
		WithOutboxWriter(noopWriter{}),
	)
	deps := cell.Dependencies{Config: make(map[string]any)}
	err := c.Init(context.Background(), deps)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestInit_SigningKeyFromConfig(t *testing.T) {
	c := NewAccessCore(
		WithOutboxWriter(noopWriter{}),
	)
	deps := cell.Dependencies{
		Config: map[string]any{
			"access.signing_key": "this-is-a-32-byte-signing-key!!!",
		},
	}
	err := c.Init(context.Background(), deps)
	// This should fail because signing key alone is insufficient (RS256 required).
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "RS256")
}
