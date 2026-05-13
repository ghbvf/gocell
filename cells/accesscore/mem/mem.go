// Package mem exposes accesscore's in-memory repository factories to
// composition roots while keeping the concrete implementations inside the
// cell's internal/mem package.
//
// Mirror of cells/accesscore/postgres for the mem/demo path.
// Composition roots import this package to construct UserRepository and
// RoleRepository backed by a single shared Store, preserving the
// cross-repo atomicity required by the effective-admin invariant (S4.0).
package mem

import (
	internalmem "github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
)

// Store is the shared backing for an in-memory accesscore deployment.
// Construct once and derive both UserRepository and RoleRepository from it
// so the embedded mutex covers cross-repo invariants (effective-admin check).
//
// Wraps cells/accesscore/internal/mem.Store.
type Store struct {
	inner *internalmem.Store
}

// NewStore constructs an empty shared Store. clk must be non-nil.
func NewStore(clk clock.Clock) *Store {
	return &Store{inner: internalmem.NewStore(clk)}
}

// UserRepository returns the UserRepository view of s.
// All instances returned from a single Store share state.
func (s *Store) UserRepository() ports.UserRepository {
	return s.inner.UserRepository()
}

// RoleRepository returns the RoleRepository view of s.
// All instances returned from a single Store share state.
func (s *Store) RoleRepository() ports.RoleRepository {
	return s.inner.RoleRepository()
}
