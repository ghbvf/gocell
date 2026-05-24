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
// # Single-lock rule with sealed lock-ownership witness (PR fix/238, #945)
//
// store.mu is the sole synchronization primitive for all map state. There are
// exactly two lock-acquisition sites:
//
//  1. memTxRunner.runLocked (the body of RunInTx) — acquires store.mu for the
//     entire transaction closure via txlock.Acquire(&store.mu). This gives all
//     tx-path operations serialized, atomic access equivalent to PG SELECT FOR
//     UPDATE held until commit.
//
//  2. Individual repository methods called OUTSIDE a held lock — each acquires
//     store.mu for its own read or write, then releases it before returning.
//
// The tx sentinel in ctx is a typed *memTxToken carrying an un-forgeable
// txlock.Held witness (see internal/txlock), NOT a bool. Only runLocked — which
// has just called txlock.Acquire(&store.mu) — injects a token whose witness
// proves THIS store's mutex is held. Repository methods skip their per-call
// lock acquisition ONLY when Store.txHoldsLock(ctx) proves the held witness
// matches store.mu by pointer identity:
//
//   - tok.held.Holds(&s.mu) : runLocked holds store.mu for the whole closure →
//     skip the per-call lock (sync.Mutex is not reentrant; re-acquiring would
//     deadlock).
//   - otherwise (no token / no-witness token / foreign store's mutex) : acquire
//     store.mu per call.
//
// Why a witness, not a bool: the original bool sentinel could not distinguish
// "RunInTx holds the lock" from "WithTxContext injected the sentinel but holds
// no lock". A non-locking TxRunner fake injecting the bool made repo methods
// skip locking with no lock actually held → concurrent map writes under
// multi-goroutine load (fatal error: concurrent map writes; the flake fixed by
// PR fix/238). The witness closes this at the type system: txlock.Held's sole
// field is unexported, so the ONLY way to obtain a Held whose Holds(&store.mu)
// is true is txlock.Acquire(&store.mu), which actually Lock()s the mutex.
// "In a tx context yet not holding the lock, so skip locking" is therefore
// inexpressible — not even inside package mem (mem may call Acquire, but Acquire
// locks; mem cannot forge a held witness through a struct literal). WithTxContext
// mints no witness, so its callers always take the per-call lock and can never
// race the maps — at the cost of no cross-method atomicity (single-goroutine
// test helpers do not need it; real cross-method atomicity requires
// Store.TxRunner()). This aligns with ent (*Tx in context), GORM (in-tx via
// ConnPool type assertion) and Kratos (*queries.Queries in context): the context
// carries a strongly-typed ownership object, never a bool. See ADR
// docs/architecture/202605171846-adr-mem-tx-lock-ownership.md.
//
// MEM-STORE-RWMUTEX-READ-CONCURRENCY: store.mu could become sync.RWMutex so
// outside-tx read methods take RLock (cf. client-go ThreadSafeStore).
// Deferred — orthogonal to the flake root fix and independently verifiable.
//
// ForUpdate variants (GetByIDForUpdate, GetByUsernameForUpdate) follow the
// same rule: inside RunInTx they read under the held store.mu (full
// FOR-UPDATE-until-commit serialization); under a foreign CellTxManager or
// WithTxContext they take store.mu per call (functional fallback, no
// cross-statement serialization). They never hard-fail on the TxRunner
// pairing — see #501 (that broke corebundle/ssobff/demo logins).
package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem/internal/txlock"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// memTxKey is the context key carrying the *memTxToken injected by
// memTxRunner.runLocked (with a held witness) or WithTxContext (no witness).
// Repository methods consult it via Store.txHoldsLock to decide whether
// store.mu is already held on the calling goroutine.
type memTxKey struct{}

// memTxToken is the typed tx-context value. It carries a txlock.Held witness
// (see internal/txlock): the ONLY way to obtain a Held that proves store.mu is
// locked is txlock.Acquire(&store.mu), which Lock()s the mutex. No code outside
// package txlock can forge a held witness (Held's field is unexported), and the
// only sanctioned Acquire site is runLocked. So "in a tx context AND holding the
// lock" is inexpressible without genuinely holding the lock — the upstream and
// downstream Hard halves of the AI-robust funnel (MEM-TX-LOCK-OWNERSHIP-01).
// WithTxContext mints a zero-witness token (Holds reports false), which never
// authorizes a lock skip. The witness's mutex identity also encodes which store
// the lock belongs to, so a separate store field is unnecessary (cross-store
// safety is the pointer comparison in Holds).
type memTxToken struct {
	// held witnesses that store.mu is locked on the calling goroutine for the
	// lifetime of the ctx (the runLocked closure). The zero Held holds nothing.
	held txlock.Held
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
// fn (via runLocked), injecting a *memTxToken whose txlock.Held witness proves
// the lock so that repository methods skip their individual lock
// acquisitions. This serializes
// all concurrent mem writes for the lifetime of fn — equivalent to a PG
// transaction WITH SELECT FOR UPDATE.
//
// Lock contract: store.mu is held from the start of fn until fn returns.
// Repository methods called from fn must not acquire store.mu (txHoldsLock
// returns true for this store, so they skip locking to avoid a deadlock).
func (r memTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	ctx, drainAfterCommit := persistence.WithAfterCommitRegistry(ctx)
	mark := persistence.AfterCommitMark(ctx)
	if err := r.runLocked(ctx, fn); err != nil {
		persistence.TruncateAfterCommitTo(ctx, mark) // discard this scope's hooks
		return err
	}
	if drainAfterCommit {
		// Drain AFTER releasing store.mu. store.mu is non-reentrant; the hook ctx
		// no longer carries the witness token (it is the outer ctx), so a hook
		// that legitimately touches the store would acquire store.mu fresh —
		// under the lock that would deadlock. Mirrors PG firing hooks after the
		// commit is durable.
		persistence.RunAfterCommitHooks(ctx)
	}
	return nil
}

// runLocked holds store.mu for the duration of fn, injecting a memTxToken whose
// txlock.Held witness proves the lock. This is the sole sanctioned txlock.Acquire
// site (MEM-TX-LOCK-OWNERSHIP-01 W1): the witness it mints is the only one that
// makes txHoldsLock return true for this store.
func (r memTxRunner) runLocked(ctx context.Context, fn func(context.Context) error) error {
	held, unlock := txlock.Acquire(&r.s.mu)
	defer unlock()
	return fn(context.WithValue(ctx, memTxKey{}, &memTxToken{held: held}))
}

// Store is the shared backing for an in-memory accesscore deployment. The
// embedded mutex protects all maps; UserRepository and RoleRepository derived
// from a single Store cooperate through this lock to implement atomic
// cross-repo invariants without leaking storage details across the repo
// boundary.
//
// TxRunner is the source of the Store-paired TxRunner that delivers full
// FOR-UPDATE-until-commit serialization. Wiring a different TxRunner (e.g.
// outbox.DemoTxRunner, or a PG tx manager in corebundle's mixed-topology e2e)
// is still functional — every repo method then takes store.mu per call —
// but the cross-statement serialization guarantee then holds only on the
// Store-TxRunner path. mem never hard-fails on the pairing (#501).
type Store struct {
	mu        sync.Mutex
	usersByID map[string]*domain.User
	byName    map[string]*domain.User
	byEmail   map[string]*domain.User        // mirrors PG users.email UNIQUE constraint
	userRoles map[string]map[string]struct{} // userID -> set of roleIDs
	roles     map[string]*domain.Role
	clock     clock.Clock
}

// txHoldsLock reports whether ctx carries a *memTxToken whose witness proves
// THIS store's mu is already held on the calling goroutine (the runLocked
// closure). Returns false for: no token, WithTxContext's zero-witness token, or
// a token minted for a different *Store's mutex. A false result means the caller
// MUST acquire store.mu for its own read/write.
//
// MEM-TX-LOCK-OWNERSHIP-01 W2 pins this exact form (`tok != nil &&
// tok.held.Holds(&s.mu)`); dropping the Holds conjunct would revive the
// "sentinel present but no lock" flake.
func (s *Store) txHoldsLock(ctx context.Context) bool {
	tok, _ := ctx.Value(memTxKey{}).(*memTxToken)
	return tok != nil && tok.held.Holds(&s.mu)
}

// NewStore constructs an empty shared Store. clk must be non-nil; mem
// repositories rely on it for timestamping CAS-guarded password updates.
func NewStore(clk clock.Clock) *Store {
	clock.MustHaveClock(clk, "mem.NewStore")
	return &Store{
		usersByID: make(map[string]*domain.User),
		byName:    make(map[string]*domain.User),
		byEmail:   make(map[string]*domain.User),
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
// for the entire RunInTx closure and injects a token whose txlock.Held witness
// proves the lock so that repository methods skip their individual lock
// acquisitions.
//
// This is the only correct TxRunner to use with repos vended by this Store.
// Composition roots and test helpers must call:
//
//	persistence.WrapForCell(store.TxRunner())
//
// Using any other TxRunner (including outbox.DemoTxRunner or a fake that injects
// the sentinel without holding store.mu) does not break safety — repo methods
// fall back to per-call locking — but it forfeits cross-method serialization.
// As a visible consequence, ChangePassword may then return
// ErrAuthOldPasswordIncorrect (if a concurrent write replaces the hash between
// the read and the bcrypt comparison) in addition to ErrVersionConflict; the
// Store-paired TxRunner's FOR-UPDATE-until-commit serialization prevents this.
func (s *Store) TxRunner() persistence.TxRunner {
	return memTxRunner{s: s}
}

// WithTxContext returns ctx carrying a zero-witness *memTxToken. Test helpers
// that need GetByIDForUpdate / GetByUsernameForUpdate to take the in-tx code
// path outside a full RunInTx (e.g. a single-goroutine test) use this. Unlike
// RunInTx, it does NOT hold store.mu and does NOT authorize skipping the
// per-call lock: every repo method invoked under this ctx still acquires
// store.mu for its own read/write. It therefore provides no cross-method
// atomicity — concurrent goroutines are each serialized per call but a
// multi-statement read-modify-write is not atomic. For real cross-method
// atomicity use Store.TxRunner().
//
// Token semantics: the injected token's held witness is the zero txlock.Held,
// whose Holds reports false for any mutex (no nil dereference; the witness is a
// value). It is structurally impossible for WithTxContext to mint a held
// witness — txlock.Acquire is the only source and it would lock the mutex. The
// use case is not limited to ForUpdate variants: any code that must enter the
// in-tx branch (e.g. a fake TxRunner injecting transaction context without
// holding the lock) can use this. Single-goroutine helpers are safe; for
// multi-goroutine scenarios requiring cross-method atomicity, use
// Store.TxRunner() instead.
func WithTxContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, memTxKey{}, &memTxToken{})
}
