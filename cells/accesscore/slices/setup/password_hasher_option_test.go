package setup

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credential"
)

// TestWithPasswordHasher_SetsAndIgnoresNil covers the setup option: a non-nil
// hasher replaces the default; a typed-nil input is ignored (builder-noop) so
// the production default survives.
func TestWithPasswordHasher_SetsAndIgnoresNil(t *testing.T) {
	t.Parallel()
	h := credential.NewTestHasher(bcrypt.MinCost)
	s := &Service{hasher: credential.NewProductionHasher()}

	WithPasswordHasher(h)(s)
	require.Equal(t, h, s.hasher, "non-nil hasher must be stored")

	var nilHasher credential.Hasher
	WithPasswordHasher(nilHasher)(s)
	require.Equal(t, h, s.hasher, "typed-nil must be ignored; previous value survives")
}
