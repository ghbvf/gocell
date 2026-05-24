package accesscoretest

import (
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore"
	"github.com/ghbvf/gocell/cells/accesscore/internal/credential"
)

// MinCostPasswordHasherOption returns an accesscore.Option that wires a
// low-cost (bcrypt.MinCost) password hasher so test harnesses that build the
// accesscore cell get fast seedAdmin / login instead of the ~1.5s/hash
// production cost. Apply it alongside the cell's other options.
//
// This is the sanctioned bridge for external test packages (e.g. the
// tests/integration harnesses) that cannot import
// cells/accesscore/internal/credential directly under Go's internal-package
// rule. It returns the fully public accesscore.Option rather than the internal
// credential.Hasher, so the public surface carries no internal type — pinned by
// external_smoke_test.go's typed gate. BCRYPT-COST-FUNNEL-01 rule A2 permits
// credential.NewTestHasher here because accesscoretest is test-support imported
// only by tests.
func MinCostPasswordHasherOption() accesscore.Option {
	return accesscore.WithPasswordHasher(credential.NewTestHasher(bcrypt.MinCost))
}
