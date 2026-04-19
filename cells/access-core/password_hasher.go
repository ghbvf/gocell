package accesscore

import "github.com/ghbvf/gocell/cells/access-core/internal/initialadmin"

// PasswordHasher re-exports initialadmin.PasswordHasher so external
// composition roots (e.g. cmd/core-bundle) can inject a test hasher
// without importing an internal package.
//
// ref: uber-go/fx — expensive production dependencies exposed as options
// so tests can swap in fast stubs without diverging from the wire graph.
type PasswordHasher = initialadmin.PasswordHasher

// BcryptHasher re-exports initialadmin.BcryptHasher. Callers construct
// it with Cost=domain.BcryptCost in production or bcrypt.MinCost in tests.
type BcryptHasher = initialadmin.BcryptHasher

// DefaultPasswordHasher returns the production bcrypt(cost=12) hasher.
// Re-exported for symmetry with BcryptHasher.
func DefaultPasswordHasher() PasswordHasher {
	return initialadmin.DefaultPasswordHasher()
}
