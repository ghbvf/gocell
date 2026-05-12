// Package mem provides in-memory repository implementations for accesscore.
//
// All UserRepository and RoleRepository instances vended by NewStore share a
// single RWMutex and underlying maps, so cross-repo invariants (notably the
// at-least-one-effective-admin invariant: S4.0) can be enforced atomically.
// This mirrors the atomicity properties of the PG adapter (advisory xact lock
// + FOR UPDATE OF u in cells/accesscore/internal/adapters/postgres/role_repo.go)
// so unit tests built on mem behave the same as the production PG path under
// concurrent mutation.
//
// There is no "standalone" mem.UserRepository / mem.RoleRepository constructor
// (S4.0 removed the per-repo NewXxx functions). All callers must go through
// NewStore to make the shared-state choice explicit and prevent accidental
// dual-store wiring that would silently lose cross-repo atomicity.
package mem

import (
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
)

// Store is the shared backing for an in-memory accesscore deployment. The
// embedded mutex protects all maps; UserRepository and RoleRepository derived
// from a single Store cooperate through this lock to implement atomic
// cross-repo invariants without leaking storage details across the repo
// boundary.
type Store struct {
	mu        sync.RWMutex
	usersByID map[string]*domain.User
	byName    map[string]*domain.User
	userRoles map[string]map[string]struct{} // userID -> set of roleIDs
	roles     map[string]*domain.Role
	clock     clock.Clock
}

// NewStore constructs an empty shared Store. clk must be non-nil; mem
// repositories rely on it for timestamping CAS-guarded password updates.
func NewStore(clk clock.Clock) *Store {
	clock.MustHaveClock(clk, "mem.NewStore")
	return &Store{
		usersByID: make(map[string]*domain.User),
		byName:    make(map[string]*domain.User),
		userRoles: make(map[string]map[string]struct{}),
		roles:     make(map[string]*domain.Role),
		clock:     clk,
	}
}

// UserRepository returns the UserRepository view of s. All instances returned
// from a single Store share state; constructing multiple Stores produces
// independent state spaces (typically wrong for production wiring).
func (s *Store) UserRepository() *UserRepository {
	return &UserRepository{store: s}
}

// RoleRepository returns the RoleRepository view of s. All instances returned
// from a single Store share state.
func (s *Store) RoleRepository() *RoleRepository {
	return &RoleRepository{store: s}
}
