// Package credential is the single sanctioned holder of accesscore password
// hashing. Every bcrypt.GenerateFromPassword call in the cell lives here
// (BCRYPT-COST-FUNNEL-01 rule A1); callers obtain a Hasher and call Hash.
//
// The bcrypt cost is fixed by which constructor minted the Hasher, not by a
// runtime parameter on the hashing call:
//
//   - NewProductionHasher() hardwires ProductionCost. There is no production
//     code path that can express a weaker cost — this is what preserves the
//     compile-time guarantee the old domain.BcryptCost const gave once cost
//     became injectable.
//   - NewTestHasher(cost) is the low-cost test door (BCRYPT-COST-FUNNEL-01 rule
//     A2 restricts its callers to *_test.go + accesscoretest). Tests pass
//     bcrypt.MinCost so the ~1.5s/hash (cost 12 under -race) does not dominate
//     unit/integration suites.
//
// Hasher is a sealed interface: the unexported isCredentialHasher marker means
// bcryptHasher is its only possible implementation. External packages can
// neither construct one nor supply a foreign (plaintext / zero-cost) hasher
// through accesscore.WithPasswordHasher — that bypass is unexpressible at the
// type level (AI-robust Hard). A missing dependency is a nil interface caught
// by the standard validateRequired() funnel (REQUIRED-DEP-NIL-GUARD-01).
//
// The seal closes only the foreign-implementation vector. The remaining
// weak-cost vector — production code calling the exported NewTestHasher with a
// low cost — is not a type-level concern and stays guarded by
// BCRYPT-COST-FUNNEL-01 rule A2's caller allowlist (upstream Medium; Hard-ization
// tracked by backlog #901). Do not claim the seal makes the whole funnel Hard.
//
// ref: go-gitea/gitea modules/auth/password/hash/bcrypt.go — cost as a hasher
// field chosen at construction; ory/kratos hash/hasher_bcrypt.go — production
// cost floor independent of the test override.
package credential

import "golang.org/x/crypto/bcrypt"

// ProductionCost is the bcrypt work factor for all production password hashing.
//
// ref: Ory Kratos BcryptDefaultCost=12; OWASP 2023 minimum recommendation.
const ProductionCost = 12

// Hasher hashes a plaintext password. Implementations are obtained from
// NewProductionHasher / NewTestHasher; the cost is bound at construction. The
// interface is sealed (isCredentialHasher) — see the package doc.
type Hasher interface {
	// Hash returns the bcrypt hash of password as a string suitable for storage
	// in User.PasswordHash. The error is bcrypt's, unwrapped — callers add their
	// own context.
	Hash(password []byte) (string, error)
	// isCredentialHasher seals the interface to this package: only bcryptHasher
	// implements it, so no external (weak / plaintext) Hasher can be wired in.
	isCredentialHasher()
}

// bcryptHasher is the only Hasher implementation. Unexported so external
// packages cannot mint one with an arbitrary (e.g. zero) cost outside the two
// sanctioned constructors.
type bcryptHasher struct{ cost int }

// NewProductionHasher returns a Hasher hardwired to ProductionCost. The
// production composition root wires this; it takes no cost argument, so the
// production path cannot select a weaker cost.
func NewProductionHasher() Hasher { return bcryptHasher{cost: ProductionCost} }

// NewTestHasher returns a Hasher at the given cost. Tests pass bcrypt.MinCost
// to keep wall-time low. BCRYPT-COST-FUNNEL-01 rule A2 restricts callers to
// test code.
func NewTestHasher(cost int) Hasher { return bcryptHasher{cost: cost} }

func (bcryptHasher) isCredentialHasher() {}

func (h bcryptHasher) Hash(password []byte) (string, error) {
	b, err := bcrypt.GenerateFromPassword(password, h.cost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
