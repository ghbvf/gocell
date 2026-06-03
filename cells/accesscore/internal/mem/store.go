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
// # Single-lock rule with a self-invalidating lock-ownership lease (#945, #972)
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
// The ctx carries a txlock.Lease value (see internal/txlock), NOT a bool and NOT
// a wrapper struct. Only runLocked — which has just called
// txlock.Acquire(&store.mu) — injects a live lease. Repository methods skip their
// per-call lock acquisition ONLY when Store.inLiveTx(ctx) reports the lease is
// live AND bound to THIS store's mutex:
//
//   - inLiveTx true (lease live, lease.mu == &store.mu) : runLocked holds store.mu
//     for the whole closure → skip the per-call lock (sync.Mutex is not reentrant;
//     re-acquiring would deadlock).
//   - otherwise (no lease / dead lease / foreign store's mutex) : acquire store.mu
//     per call.
//
// Two AI-robust axes, both carried by the sealed txlock package (not archtest):
//
//   - Forge (upstream Hard): txlock.Lease's fields are unexported, so the ONLY
//     way to obtain a lease whose Live(&store.mu) is true is
//     txlock.Acquire(&store.mu), which actually Lock()s the mutex. "In a tx
//     context yet not holding the lock, so skip locking" is inexpressible — not
//     even inside package mem (mem may call Acquire, but Acquire locks; mem cannot
//     forge a live lease through a struct literal).
//   - Liveness (downstream Hard): the lease self-invalidates. Acquire's unlock
//     closure (the sole writer of the live flag) flips it dead before store.mu is
//     released. A ctx that escapes the runLocked closure — captured by a goroutine
//     or an after-commit hook — therefore reports inLiveTx==false afterwards and
//     falls back to per-call locking (fail-closed). This closes the
//     capability-escape that the earlier pointer-only witness left open (#972): the
//     witness proved the mutex was locked at SOME point, never that it is locked
//     NOW. Mirrors database/sql *Tx.done → ErrTxDone invalidation on completion.
//
// History: the original bool sentinel (pre-#945) could not distinguish "RunInTx
// holds the lock" from "a non-locking fake injected the sentinel"; a fake making
// repo methods skip locking with no lock held caused concurrent map writes under
// multi-goroutine load (fatal error: concurrent map writes; flake fixed by PR
// fix/238 → #945 witness → #972 lease). The model aligns with ent (*Tx in
// context), GORM (in-tx via ConnPool type assertion) and Kratos
// (*queries.Queries in context): the context carries a strongly-typed,
// resource-bound ownership object, never a bool. See ADR
// docs/architecture/202605171846-adr-mem-tx-lock-ownership.md.
//
// Concurrency boundary: the lease guards AFTER-RETURN escape, not
// CONCURRENT-DURING-TX use. Two goroutines sharing one LIVE tx ctx (e.g. a
// goroutine spawned inside the closure that touches the repo with the inner ctx
// while the parent still holds the lock) both see inLiveTx==true and skip
// locking → the non-holder races the maps. This is the database/sql "*Tx must not
// be used concurrently by multiple goroutines" boundary and is out of scope for
// the lease, exactly as ErrTxDone does not make *sql.Tx concurrency-safe.
// Operational rule: a goroutine spawned inside the closure MUST NOT use the inner
// ctx to call repository methods — pass context.Background() (or hand results back
// over a channel) so the goroutine takes its own per-call lock.
//
// MEM-STORE-RWMUTEX-READ-CONCURRENCY: store.mu could become sync.RWMutex so
// outside-tx read methods take RLock (cf. client-go ThreadSafeStore).
// Deferred — orthogonal to the flake root fix and independently verifiable.
//
// ForUpdate variants (GetByIDForUpdate, GetByUsernameForUpdate) follow the
// same rule: inside RunInTx they read under the held store.mu (full
// FOR-UPDATE-until-commit serialization); under a foreign CellTxManager they take
// store.mu per call (functional fallback, no cross-statement serialization). They
// never hard-fail on the TxRunner pairing — see #501 (that broke
// corebundle/ssobff/demo logins).
package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem/internal/txlock"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// memTxKey is the context key carrying the txlock.Lease injected by
// memTxRunner.runLocked. A LIVE lease authorizes repository methods to skip their
// per-call store.mu lock (store.mu is held for the whole runLocked closure); the
// zero Lease (absent key) means the method must take its own lock. Repository
// methods consult it via Store.inLiveTx.
//
// The ctx carries the txlock.Lease value directly — no wrapper struct. The ONLY
// way to obtain a Lease whose Live(&store.mu) is true is txlock.Acquire(&store.mu)
// (which Lock()s the mutex and arms the live flag), and the sole sanctioned
// Acquire site is runLocked. So "in a tx context AND holding the lock" is
// inexpressible without genuinely holding the lock (upstream Hard), and the lease
// self-invalidates when unlock runs so a ctx that escapes the closure fails
// closed (downstream Hard) — see internal/txlock.
type memTxKey struct{}

// memTxRunner is a Store-bound TxRunner that acquires store.mu for the entire
// transaction closure. All repository operations invoked from within the
// closure run without additional locking (inLiveTx proves the lock is
// held), serializing them atomically — equivalent to PG SELECT FOR UPDATE
// held until commit.
//
// Store-bound invariant: memTxRunner.s must be the same Store that vended the
// UserRepository / RoleRepository objects passed to service constructors.
// Store.TxRunner() is the only factory; there is no public constructor.
type memTxRunner struct{ s *Store }

// RunInTx acquires store.mu (exclusive write lock) for the entire duration of
// fn (via runLocked), injecting a live txlock.Lease so that repository methods
// skip their individual lock acquisitions. This serializes all concurrent mem
// writes for the lifetime of fn — equivalent to a PG transaction WITH SELECT FOR
// UPDATE.
//
// Lock contract: store.mu is held from the start of fn until fn returns.
// Repository methods called from fn must not acquire store.mu (inLiveTx returns
// true for this store, so they skip locking to avoid a deadlock).
//
// Nested RunInTx (e.g. inside an after-commit hook): WithAfterCommitRegistry
// returns drainAfterCommit=false when a registry is already present in ctx, so
// hooks registered by a RunInTx invoked from within a hook accumulate into the
// outer registry and fire in the outer drain pass, not from the nested call.
// (Upstream kernel/persistence semantics, unchanged by the lease rework.)
func (r memTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	ctx, drainAfterCommit := persistence.WithAfterCommitRegistry(ctx)
	mark := persistence.AfterCommitMark(ctx)
	if err := r.runLocked(ctx, fn); err != nil {
		persistence.TruncateAfterCommitTo(ctx, mark) // discard this scope's hooks
		return err
	}
	if drainAfterCommit {
		// Drain AFTER releasing store.mu. store.mu is non-reentrant; the drain ctx
		// is the outer ctx (no lease), so a hook that legitimately touches the
		// store acquires store.mu fresh. Even a hook that captured the inner ctx
		// is safe now: runLocked's defer unlock() flipped the lease dead before
		// this point (#972), so inLiveTx reports false on the captured ctx and the
		// hook takes the per-call lock instead of stale-skipping. Mirrors PG firing
		// hooks after the commit is durable.
		persistence.RunAfterCommitHooks(ctx)
	}
	return nil
}

// runLocked holds store.mu for the duration of fn, injecting the txlock.Lease
// that proves the lock. This is the sole sanctioned txlock.Acquire site
// (MEM-TX-LOCK-OWNERSHIP-01 W1): the lease it mints is the only one that makes
// inLiveTx return true for this store, and it self-invalidates when unlock runs,
// so an inner ctx that escapes this frame reports inLiveTx==false afterwards and
// falls back to per-call locking (#972).
func (r memTxRunner) runLocked(ctx context.Context, fn func(context.Context) error) error {
	lease, unlock := txlock.Acquire(&r.s.mu)
	defer unlock()
	return fn(context.WithValue(ctx, memTxKey{}, lease))
}

// Store is the shared backing for an in-memory accesscore deployment. The
// embedded mutex protects all maps; UserRepository and RoleRepository derived
// from a single Store cooperate through this lock to implement atomic
// cross-repo invariants without leaking storage details across the repo
// boundary.
//
// # Tenancy layout (#1337 PR-2)
//
// To mirror the PG schema (tenant-scoped unique indices), the following maps
// use tenant-scoped keys:
//
//   - usersByID: global UUID PK (not tenant-scoped — mirrors PG users.id PK)
//   - byName: [tenantID][username] → *User (composite unique per tenant)
//   - byEmail: [tenantID][email]   → *User (composite unique per tenant)
//   - userRoles: [tenantID][userID] → set of roleIDs (per-tenant assignments)
//   - roles: [tenantID][roleID] → *Role (per-tenant role definitions)
//
// TxRunner is the source of the Store-paired TxRunner that delivers full
// FOR-UPDATE-until-commit serialization. Wiring a different TxRunner (e.g.
// outbox.DemoTxRunner, or a PG tx manager in corebundle's mixed-topology e2e)
// is still functional — every repo method then takes store.mu per call —
// but the cross-statement serialization guarantee then holds only on the
// Store-TxRunner path. mem never hard-fails on the pairing (#501).
type Store struct {
	mu        sync.Mutex
	usersByID map[string]*domain.User                   // id → User (global PK, tenant-deriving)
	byName    map[string]map[string]*domain.User        // tenantID → username → User
	byEmail   map[string]map[string]*domain.User        // tenantID → email    → User
	userRoles map[string]map[string]map[string]struct{} // tenantID → userID   → set of roleIDs
	roles     map[string]map[string]*domain.Role        // tenantID → roleID   → Role
	clock     clock.Clock
}

// inLiveTx reports whether ctx carries a LIVE txlock.Lease proving THIS store's
// mu is held on the calling goroutine (inside the runLocked closure). Returns
// false for: no lease (absent key → zero Lease), a lease minted for a different
// *Store's mutex, or a lease whose tx already returned (unlock flipped it dead —
// #972). A false result means the caller MUST acquire store.mu for its own
// read/write.
//
// MEM-TX-LOCK-OWNERSHIP-01 W2 pins this exact form (single return of
// `l.Live(&s.mu)` where l is the type-asserted lease); the identity + liveness
// logic lives in the sealed, reflect-frozen txlock.Lease.Live. The zero Lease
// from a failed type assertion is safe (Live's mu/live nil-guards report false).
func (s *Store) inLiveTx(ctx context.Context) bool {
	l, _ := ctx.Value(memTxKey{}).(txlock.Lease)
	return l.Live(&s.mu)
}

// NewStore constructs an empty shared Store. clk must be non-nil; mem
// repositories rely on it for timestamping CAS-guarded password updates.
func NewStore(clk clock.Clock) *Store {
	clock.MustHaveClock(clk, "mem.NewStore")
	return &Store{
		usersByID: make(map[string]*domain.User),
		byName:    make(map[string]map[string]*domain.User),
		byEmail:   make(map[string]map[string]*domain.User),
		userRoles: make(map[string]map[string]map[string]struct{}),
		roles:     make(map[string]map[string]*domain.Role),
		clock:     clk,
	}
}

// tenantRoles returns the role map for a tenant (lazy init). Caller must hold store.mu.
func (s *Store) tenantRoles(tenantID string) map[string]*domain.Role {
	if s.roles[tenantID] == nil {
		s.roles[tenantID] = make(map[string]*domain.Role)
	}
	return s.roles[tenantID]
}

// tenantUserRoles returns the userID→roleSet map for a tenant (lazy init). Caller must hold store.mu.
func (s *Store) tenantUserRoles(tenantID string) map[string]map[string]struct{} {
	if s.userRoles[tenantID] == nil {
		s.userRoles[tenantID] = make(map[string]map[string]struct{})
	}
	return s.userRoles[tenantID]
}

// tenantByName returns the username→User map for a tenant (lazy init). Caller must hold store.mu.
func (s *Store) tenantByName(tenantID string) map[string]*domain.User {
	if s.byName[tenantID] == nil {
		s.byName[tenantID] = make(map[string]*domain.User)
	}
	return s.byName[tenantID]
}

// tenantByEmail returns the email→User map for a tenant (lazy init). Caller must hold store.mu.
func (s *Store) tenantByEmail(tenantID string) map[string]*domain.User {
	if s.byEmail[tenantID] == nil {
		s.byEmail[tenantID] = make(map[string]*domain.User)
	}
	return s.byEmail[tenantID]
}

// userByIDInTenant is the tenant-scoped by-global-PK lookup used by every
// tenant-scoped write method. It mirrors the PG `WHERE tenant_id = $N AND id = $1`
// predicate: a user present in the global usersByID index but belonging to a
// different tenant is reported as absent (ok=false), so cross-tenant access
// collapses to the method's own not-found path. The GetByID carve-out
// (tenant-deriving by global PK) intentionally does NOT use this. Caller must
// hold store.mu.
func (s *Store) userByIDInTenant(userID, tenantID string) (*domain.User, bool) {
	u, ok := s.usersByID[userID]
	if !ok || string(u.TenantID) != tenantID {
		return nil, false
	}
	return u, true
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
// for the entire RunInTx closure and injects a live txlock.Lease so that
// repository methods skip their individual lock acquisitions.
//
// This is the only correct TxRunner to use with repos vended by this Store.
// Composition roots and test helpers must call:
//
//	persistence.WrapForCell(store.TxRunner())
//
// Using any other TxRunner (including outbox.DemoTxRunner or a fake that does not
// hold store.mu) does not break safety — repo methods fall back to per-call
// locking (no live lease in ctx) — but it forfeits cross-method serialization.
// As a visible consequence, ChangePassword may then return
// ErrAuthOldPasswordIncorrect (if a concurrent write replaces the hash between
// the read and the bcrypt comparison) in addition to ErrVersionConflict; the
// Store-paired TxRunner's FOR-UPDATE-until-commit serialization prevents this.
func (s *Store) TxRunner() persistence.TxRunner {
	return memTxRunner{s: s}
}
