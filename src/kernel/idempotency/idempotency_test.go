package idempotency

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Compile-time interface check.

type mockChecker struct{}

func (m *mockChecker) IsProcessed(ctx context.Context, key string) (bool, error) {
	return false, nil
}

func (m *mockChecker) MarkProcessed(ctx context.Context, key string, ttl time.Duration) error {
	return nil
}

var _ Checker = (*mockChecker)(nil)

func TestCheckerInterface(t *testing.T) {
	var c Checker = &mockChecker{}
	ok, err := c.IsProcessed(context.Background(), "test-key")
	assert.NoError(t, err)
	assert.False(t, ok)
}
