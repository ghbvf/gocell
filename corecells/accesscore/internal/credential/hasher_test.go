package credential

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// TestConstructors_BindCost asserts each constructor binds the intended cost.
// White-box on the bcryptHasher impl so it does not pay a real cost-12 hash
// (NewProductionHasher's cost is verified by field, not by hashing).
func TestConstructors_BindCost(t *testing.T) {
	t.Parallel()
	assert.Equal(t, ProductionCost, NewProductionHasher().(bcryptHasher).cost,
		"NewProductionHasher must hardwire ProductionCost (no cost knob)")
	assert.Equal(t, bcrypt.MinCost, NewTestHasher(bcrypt.MinCost).(bcryptHasher).cost,
		"NewTestHasher must bind the given cost")
}

// TestHash_RoundTrip exercises the Hash success path: the output is a valid
// bcrypt hash at the bound cost that verifies against the original password and
// rejects a wrong one.
func TestHash_RoundTrip(t *testing.T) {
	t.Parallel()
	h := NewTestHasher(bcrypt.MinCost)
	pw := []byte("correct-horse-battery-staple")

	hash, err := h.Hash(pw)
	require.NoError(t, err)

	cost, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	assert.Equal(t, bcrypt.MinCost, cost)

	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), pw),
		"hash must verify against the original password")
	require.Error(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("wrong")),
		"hash must reject a different password")
}

// TestHash_Error covers the Hash error branch: bcrypt rejects inputs longer
// than 72 bytes, so Hash must surface that error rather than a partial hash.
func TestHash_Error(t *testing.T) {
	t.Parallel()
	h := NewTestHasher(bcrypt.MinCost)

	out, err := h.Hash([]byte(strings.Repeat("x", 73)))
	require.Error(t, err)
	require.ErrorIs(t, err, bcrypt.ErrPasswordTooLong)
	assert.Empty(t, out, "Hash must return an empty string on error")
}
