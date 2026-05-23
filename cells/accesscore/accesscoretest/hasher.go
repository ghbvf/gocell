package accesscoretest

import (
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credential"
)

// MinCostPasswordHasher returns a low-cost (bcrypt.MinCost) password hasher for
// tests that build the accesscore cell and want fast seedAdmin / login. Pass it
// to accesscore.WithPasswordHasher.
//
// This is the sanctioned bridge for external test packages (e.g. the
// tests/integration harnesses) that cannot import cells/accesscore/internal/credential
// directly under Go's internal-package rule. BCRYPT-COST-FUNNEL-01 rule A2
// permits credential.NewTestHasher here because accesscoretest is test-support
// imported only by tests.
func MinCostPasswordHasher() credential.Hasher {
	return credential.NewTestHasher(bcrypt.MinCost)
}
