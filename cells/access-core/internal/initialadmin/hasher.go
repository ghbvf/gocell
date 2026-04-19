//go:build unix

package initialadmin

import (
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
)

// PasswordHasher abstracts the password-hashing step so tests can inject a
// fast (low-cost bcrypt) hasher without paying the production-strength cost
// on every BuildBootstrap startup. Production code uses DefaultPasswordHasher
// which wraps bcrypt at domain.BcryptCost (12).
//
// The interface is deliberately narrow: only Hash is needed during the
// initial-admin bootstrap path. Password verification on login uses
// bcrypt.CompareHashAndPassword directly — cost is encoded in the hash and
// does not need an injection point.
//
// ref: AWS Cognito / Keycloak — hashing algorithm + cost is a deployment
// knob, not a hardcoded constant. Unit tests override with a fast variant
// (bcrypt.MinCost or a deterministic stub) to keep CI predictable.
type PasswordHasher interface {
	// Hash returns the bcrypt-compatible hash of password. Callers zero the
	// plaintext slice after return; implementations must not retain it.
	Hash(password []byte) (hash []byte, err error)
}

// BcryptHasher is the production PasswordHasher. Cost must be within
// bcrypt.MinCost..bcrypt.MaxCost; callers should use domain.BcryptCost in
// production or bcrypt.MinCost in unit tests.
type BcryptHasher struct {
	Cost int
}

// Hash implements PasswordHasher using golang.org/x/crypto/bcrypt.
func (b BcryptHasher) Hash(password []byte) ([]byte, error) {
	return bcrypt.GenerateFromPassword(password, b.Cost)
}

// DefaultPasswordHasher returns the production hasher: bcrypt at
// domain.BcryptCost (OWASP 2023 minimum 12).
func DefaultPasswordHasher() PasswordHasher {
	return BcryptHasher{Cost: domain.BcryptCost}
}
