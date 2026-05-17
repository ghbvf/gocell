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
// # Single-lock rule (R1) with lock-ownership truth (PR fix/238)
//
// store.mu is the sole synchronization primitive for all map state. There are
// exactly two lock-acquisition sites:
//
//  1. memTxRunner.RunInTx — acquires store.mu for the entire transaction
//     closure. This gives all tx-path operations serialized, atomic access
//     equivalent to PG SELECT FOR UPDATE held until commit.
//
//  2. Individual repository methods called OUTSIDE a held lock — each acquires
//     store.mu for its own read or write, then releases it before returning.
//
// The tx sentinel in ctx is a typed *memTxToken, NOT a bool. Only RunInTx —
// which has *just called store.mu.Lock() — injects a token with
// holdsLock=true bound to its own *Store. Repository methods skip their
// per-call lock acquisition ONLY when Store.txHoldsLock(ctx) proves that THIS
// store's mutex is genuinely held on the calling goroutine:
//
//   - holdsLock==true && token.store==s : RunInTx holds store.mu for the
//     whole closure → skip the per-call lock (sync.Mutex is not reentrant;
//     re-acquiring would deadlock).
//   - otherwise (no token / holdsLock==false / foreign store) : acquire
//     store.mu per call.
//
// Why a typed token, not a bool: the previous bool sentinel could not
// distinguish "RunInTx holds the lock" from "WithTxContext injected the
// sentinel but holds no lock". A non-locking TxRunner fake injecting the bool
// made repo methods skip locking with no lock actually held → concurrent map
// writes under multi-goroutine load (fatal error: concurrent map writes; the
// flake fixed by PR fix/238). With holdsLock as an unexported field whose
// only true-construction site is memTxRunner.RunInTx in this file (which
// really holds the lock), "in a tx context yet not holding the lock, so skip
// locking" is no longer expressible by any code outside package mem.
// WithTxContext yields holdsLock=false, so its callers always take the
// per-call lock and can never race the maps — at the cost of no cross-method
// atomicity (single-goroutine test helpers do not need it; real cross-method
// atomicity requires Store.TxRunner()). This aligns with ent (*Tx in
// context), GORM (in-tx via ConnPool type assertion) and Kratos
// (*queries.Queries in context): the context carries a strongly-typed
// ownership object, never a bool. See ADR
// docs/architecture/202605171846-adr-mem-tx-lock-ownership.md.
//
// Backlog MEM-STORE-RWMUTEX-READ-CONCURRENCY (docs/backlog/cap-14-tooling.md):
// store.mu could become sync.RWMutex so outside-tx read methods take RLock
// (cf. client-go ThreadSafeStore). Deferred — orthogonal to the flake root
// fix and independently verifiable; recorded, not silently carried over.
//
// ForUpdate variants (GetByIDForUpdate, GetByUsernameForUpdate) follow the
// same rule: inside memTxRunner.RunInTx they read under the held store.mu
// (full FOR-UPDATE-until-commit serialization); under a foreign CellTxManager
// or WithTxContext they take store.mu per call (functional fallback, no
// cross-statement serialization). They never hard-fail on the TxRunner
// pairing — see #501 (that broke corebundle/ssobff/demo logins).
package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// memTxKey is the context key carrying the *memTxToken injected by
// memTxRunner.RunInTx (holdsLock=true) or WithTxContext (holdsLock=false).
// Repository methods consult it via Store.txHoldsLock to decide whether
// store.mu is already held on the calling goroutine.
type memTxKey struct{}

// memTxToken is the typed tx-context value. It is unexported and its fields
// are unexported: the ONLY site that can construct a token with
// holdsLock=true is memTxRunner.RunInTx in this file (which has just called
// store.mu.Lock()). No code outside package mem — including test TxRunner
// fakes — can express "in a tx context AND holding the lock"; the only
// out-of-package entry, WithTxContext, hard-codes holdsLock=false. This is
// the upstream half of the AI-rebust Hard funnel (MEM-TX-LOCK-OWNERSHIP-01).
type memTxToken struct {
	// store identifies which *Store's mutex the holder claims to hold. A
	// token minted for store A must not let store B's repo methods skip
	// their lock (cross-store safety).
	store *Store
	// holdsLock is true iff the injector currently holds store.mu on the
	// calling goroutine for the lifetime of the ctx (the RunInTx closure).
	holdsLock bool
}

// memTxRunner is a Store-bound TxRunner that acquires store.mu for the entire
// transaction closure. All repository operations invoked from within the
// closure run without additional locking (txHoldsLock proves the lock is
// held), serializing them atomically — equivalent to PG SELECT FOR UPDATE
// held until commit.
//
// Store-bound invariant: memTxRunner.s must be the same Store that vended the
// UserRepository / RoleRepository objects passed to service constructors.
// Store.TxRunner() is the only factory; there is no public constructor.
type memTxRunner struct{ s *Store }

// RunInTx acquires store.mu (exclusive write lock) for the entire duration of
// fn, injecting a holdsLock=true *memTxToken bound to this store so that
// repository methods skip their individual lock acquisitions. This serializes
// all concurrent mem writes for the lifetime of fn — equivalent to a PG
// transaction WITH SELECT FOR UPDATE.
//
// Lock contract: store.mu is held from the start of fn until fn returns.
// Repository methods called from fn must not acquire store.mu (txHoldsLock
// returns true for this store, so they skip locking to avoid a deadlock).
func (r memTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return fn(context.WithValue(ctx, memTxKey{}, &memTxToken{store: r.s, holdsLock: true}))
}

// Store is the shared backing for an in-memory accesscore deployment. The
// embedded mutex protects all maps; UserRepository and RoleRepository derived
// from a single Store cooperate through this lock to implement atomic
// cross-repo invariants without leaking storage details across the repo
// boundary.
//
// TxRunner is the source of the Store-paired TxRunner that delivers full
// FOR-UPDATE-until-commit serialization. Wiring a different TxRunner (e.g.
// cell.DemoTxRunner, or a PG tx manager in corebundle's mixed-topology e2e)
// is still functional — every repo method then takes store.mu per call —
// but the cross-statement serialization guarantee then holds only on the
// Store-TxRunner path. mem never hard-fails on the pairing (#501).
type Store struct {
	mu        sync.Mutex
	usersByID map[string]*domain.User
	byName    map[string]*domain.User
	userRoles map[string]map[string]struct{} // userID -> set of roleIDs
	roles     map[string]*domain.Role
	clock     clock.Clock
}

// txHoldsLock reports whether ctx carries a *memTxToken proving that THIS
// store's mu is already held on the calling goroutine (the RunInTx closure).
// Returns false for: no token, WithTxContext's holdsLock=false token, or a
// token minted for a different *Store. A false result means the caller MUST
// acquire store.mu for its own read/write.
func (s *Store) txHoldsLock(ctx context.Context) bool {
	tok, _ := ctx.Value(memTxKey{}).(*memTxToken)
	return tok != nil && tok.holdsLock && tok.store == s
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
// for the entire RunInTx closure and injects a holdsLock=true token so that
// repository methods skip their individual lock acquisitions.
//
// This is the only correct TxRunner to use with repos vended by this Store.
// Composition roots and test helpers must call:
//
//	persistence.WrapForCell(store.TxRunner())
//
// Using any other TxRunner (including cell.DemoTxRunner or a fake that injects
// the sentinel without holding store.mu) does not break safety — repo methods
// fall back to per-call locking — but it forfeits cross-method serialization.
func (s *Store) TxRunner() persistence.TxRunner {
	return memTxRunner{s: s}
}

// WithTxContext returns ctx carrying a holdsLock=false *memTxToken. Test
// helpers that need GetByIDForUpdate / GetByUsernameForUpdate to take the
// in-tx code path outside a full RunInTx (e.g. a single-goroutine test) use
// this. Unlike RunInTx, it does NOT hold store.mu and does NOT authorize
// skipping the per-call lock: every repo method invoked under this ctx still
// acquires store.mu for its own read/write. It therefore provides no
// cross-method atomicity — concurrent goroutines are each serialized per
// call but a multi-statement read-modify-write is not atomic. For real
// cross-method atomicity use Store.TxRunner().
//
// Token semantics: the injected token has store==nil and holdsLock==false.
// txHoldsLock checks tok.holdsLock first, so the nil store is never
// dereferenced (short-circuit: false && ... = false). Callers must not read
// token.store for any purpose — it is nil by design in this path. The use
// case is not limited to ForUpdate variants: any code that must enter the
// in-tx branch (e.g. a fake TxRunner injecting transaction context without
// holding the lock) can use this. Single-goroutine helpers are safe; for
// multi-goroutine scenarios requiring cross-method atomicity, use
// Store.TxRunner() instead.
func WithTxContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, memTxKey{}, &memTxToken{holdsLock: false})
}
