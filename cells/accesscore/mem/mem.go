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
	"github.com/ghbvf/gocell/kernel/persistence"
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

// TxRunner returns the Store-paired persistence.TxRunner. Composition roots
// must pass this value (wrapped via persistence.WrapForCell) to
// accesscore.WithTxManager so that the setup service's RunInTx body acquires
// store.mu for the entire first-admin provisioning closure — serializing
// concurrent setup requests within a single process.
//
// Using any other TxRunner (including cell.DemoTxRunner) forfeits this
// serialization: concurrent first-admin requests can then both pass the
// CountByRole==0 fast-path before either commits, producing two admins
// (TOCTOU). Always wire:
//
//	accesscore.WithTxManager(persistence.WrapForCell(userMemStore.TxRunner()))
func (s *Store) TxRunner() persistence.TxRunner {
	return s.inner.TxRunner()
}
