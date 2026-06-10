package accesscore

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/credential"
)

// TestWithPasswordHasher_SetsAndIgnoresNil covers the cell-level option: a
// non-nil hasher replaces the default, and a typed-nil input is ignored so the
// production default survives (builder-noop semantics).
func TestWithPasswordHasher_SetsAndIgnoresNil(t *testing.T) {
	t.Parallel()
	h := credential.NewTestHasher(bcrypt.MinCost)
	c := &AccessCore{passwordHasher: credential.NewProductionHasher()}

	WithPasswordHasher(h)(c)
	require.Equal(t, h, c.passwordHasher, "non-nil hasher must be stored")

	var nilHasher credential.Hasher
	WithPasswordHasher(nilHasher)(c)
	require.Equal(t, h, c.passwordHasher, "typed-nil must be ignored; previous value survives")
}
