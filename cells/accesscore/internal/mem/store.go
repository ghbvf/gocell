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
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// memTxKey is the context key injected by Store.TxRunner when entering a
// RunInTx body. GetByIDForUpdate and GetByUsernameForUpdate check for this
// key to enforce that FOR-UPDATE semantics are only invoked inside a logical
// transaction context — mirroring assertAmbientTx in the PG adapter.
type memTxKey struct{}

// memTxRunner is a simple synchronous TxRunner backed by a Store. It injects
// memTxKey into ctx before calling the function body so that
// GetByIDForUpdate / GetByUsernameForUpdate can fail-fast when called outside
// a RunInTx boundary.
type memTxRunner struct{}

func (memTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(context.WithValue(ctx, memTxKey{}, true))
}

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

// TxRunner returns a persistence.TxRunner that marks the context with a
// mem-tx sentinel before invoking the callback. The repositories vended by
// this Store detect the sentinel in GetByIDForUpdate /
// GetByUsernameForUpdate and succeed; calls without the sentinel fail-fast
// with errcode.ErrInternal — matching the PG assertAmbientTx contract.
//
// Usage: persistence.WrapForCell(store.TxRunner()) at the composition root
// or in test helpers that exercise FOR-UPDATE paths.
func (s *Store) TxRunner() persistence.TxRunner {
	return memTxRunner{}
}

// WithTxContext returns ctx with the mem-tx sentinel injected. Test helpers
// that use a custom TxRunner (e.g. recordingTxRunner, snapshotTxRunner) and
// need to call GetByIDForUpdate / GetByUsernameForUpdate must wrap the
// context they pass to fn with this function:
//
//	func (r *myTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
//	    return fn(mem.WithTxContext(ctx))
//	}
func WithTxContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, memTxKey{}, true)
}
