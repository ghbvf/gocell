// Package mem provides in-memory repository implementations for accesscore.
//
// # Locking model
//
// All UserRepository and RoleRepository instances vended by NewStore share a
// single sync.Mutex (store.mu) and underlying maps, so cross-repo invariants
// (notably the at-least-one-effective-admin invariant: S4.0) can be enforced
// atomically. This mirrors the atomicity properties of the PG adapter
// (advisory xact lock + FOR UPDATE OF u in
// cells/accesscore/internal/adapters/postgres/role_repo.go) so unit tests
// built on mem behave the same as the production PG path under concurrent
// mutation.
//
// There is no "standalone" mem.UserRepository / mem.RoleRepository constructor
// (S4.0 removed the per-repo NewXxx functions). All callers must go through
// NewStore to make the shared-state choice explicit and prevent accidental
// dual-store wiring that would silently lose cross-repo atomicity.
//
// # Single-lock rule (R1)
//
// store.mu is the sole synchronization primitive for all map state. There are
// exactly two lock acquisition sites:
//
//  1. memTxRunner.RunInTx — acquires store.mu for the entire transaction
//     closure. This gives all tx-path operations serialized, atomic access
//     equivalent to PG SELECT FOR UPDATE held until commit.
//
//  2. Individual repository methods called OUTSIDE a tx — each acquires
//     store.mu for its own read or write, then releases it before returning.
//
// Methods detect which regime they are in via isInMemTx(ctx):
//
//   - inside tx (sentinel present): do NOT acquire store.mu — the lock is
//     already held by RunInTx on the same goroutine (sync.Mutex is not
//     reentrant; re-acquiring would deadlock).
//   - outside tx (no sentinel): acquire store.mu as usual.
//
// ForUpdate variants (GetByIDForUpdate, GetByUsernameForUpdate) additionally
// enforce that they are always called inside a tx via assertMemTx; they never
// acquire the lock themselves (the tx body already holds it).
package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// memTxKey is the context key injected by memTxRunner.RunInTx when entering a
// transaction body. Repository methods check for this key via isInMemTx to
// decide whether to acquire store.mu (outside tx) or skip locking (inside tx,
// where RunInTx already holds the lock on the same goroutine).
type memTxKey struct{}

// isInMemTx reports whether ctx carries the mem-tx sentinel injected by
// memTxRunner.RunInTx. Used by repository methods to skip lock acquisition
// when called from within a RunInTx closure (RunInTx already holds store.mu).
func isInMemTx(ctx context.Context) bool {
	v, _ := ctx.Value(memTxKey{}).(bool)
	return v
}

// memTxRunner is a Store-bound TxRunner that acquires store.mu for the entire
// transaction closure. All repository operations invoked from within the
// closure run without additional locking (they detect the sentinel and skip
// their own lock acquisition), serializing them atomically — equivalent to PG
// SELECT FOR UPDATE held until commit.
//
// Store-bound invariant: memTxRunner.s must be the same Store that vended the
// UserRepository / RoleRepository objects passed to service constructors.
// Store.TxRunner() is the only factory; there is no public constructor.
type memTxRunner struct{ s *Store }

// RunInTx acquires store.mu (exclusive write lock) for the entire duration of
// fn, injecting the mem-tx sentinel into ctx so that repository methods skip
// their individual lock acquisitions. This serializes all concurrent mem writes
// for the lifetime of fn — equivalent to PG transaction WITH SELECT FOR UPDATE.
//
// Lock contract: store.mu is held from the start of fn until fn returns.
// Repository methods called from fn must not acquire store.mu (they detect the
// sentinel via isInMemTx and skip locking to avoid a deadlock).
func (r memTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return fn(context.WithValue(ctx, memTxKey{}, true))
}

// Store is the shared backing for an in-memory accesscore deployment. The
// embedded mutex protects all maps; UserRepository and RoleRepository derived
// from a single Store cooperate through this lock to implement atomic
// cross-repo invariants without leaking storage details across the repo
// boundary.
//
// TxRunner is the only source of a correctly-paired TxRunner for this Store.
// Wiring a different TxRunner (e.g. cell.DemoTxRunner) with repos from this
// Store breaks the single-lock rule and will cause assertMemTx to fail-fast on
// any ForUpdate call.
type Store struct {
	mu        sync.Mutex
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

// TxRunner returns a Store-bound persistence.TxRunner. It acquires store.mu
// for the entire RunInTx closure and injects the mem-tx sentinel so that
// repository methods skip their individual lock acquisitions.
//
// This is the only correct TxRunner to use with repos vended by this Store.
// Composition roots and test helpers must call:
//
//	persistence.WrapForCell(store.TxRunner())
//
// Using any other TxRunner (including cell.DemoTxRunner or a stub that injects
// the sentinel without holding store.mu) breaks the serialization guarantee.
func (s *Store) TxRunner() persistence.TxRunner {
	return memTxRunner{s: s}
}

// WithTxContext returns ctx with the mem-tx sentinel injected. Test helpers
// that need to call GetByIDForUpdate / GetByUsernameForUpdate outside a full
// RunInTx (e.g. to bypass locking in a single-goroutine test) must use this
// function — but note that, unlike RunInTx, it does NOT hold store.mu, so
// concurrent-safety is the caller's responsibility.
func WithTxContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, memTxKey{}, true)
}
