package accesscoretest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// TestMinCostPasswordHasher_UsesMinCost asserts the bridge returns a hasher
// bound to bcrypt.MinCost so test harnesses get fast hashing.
func TestMinCostPasswordHasher_UsesMinCost(t *testing.T) {
	t.Parallel()
	hash, err := MinCostPasswordHasher().Hash([]byte("pw"))
	require.NoError(t, err)
	cost, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	assert.Equal(t, bcrypt.MinCost, cost)
}
