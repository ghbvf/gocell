// Package txlock mints un-forgeable, self-invalidating lock-ownership leases for
// the mem accesscore store. It is the upstream+downstream Hard half of the mem tx
// lock-ownership funnel (MEM-TX-LOCK-OWNERSHIP-01; ADR
// docs/architecture/202605171846-adr-mem-tx-lock-ownership.md).
//
// The funnel exists to make the "sentinel present but no lock" flake
// (PR #558 concurrent-map-write) compile-impossible rather than archtest-caught:
// a tx-context lease may authorize a repository method to skip its per-call
// store.mu lock ONLY while the lock is genuinely held on the calling goroutine.
//
// Lease is the proof. Both fields are unexported, so:
//
//   - No package outside txlock can write txlock.Lease{mu: …, live: …} — the
//     composite-literal fields are inaccessible. The empty literal txlock.Lease{}
//     and the zero value carry mu == nil / live == nil, and Live reports false for
//     any mutex.
//   - The ONLY way to obtain a Lease whose Live(&store.mu) is true is to call
//     Acquire(&store.mu), which actually Lock()s the mutex AND arms the live flag.
//
// # Forge axis (upstream Hard)
//
// "In a tx context AND holding the lock" cannot be expressed without having
// locked — not even inside package mem (mem can call Acquire, but Acquire locks;
// mem cannot forge a live lease via a struct literal because mu/live are
// unexported).
//
// # Liveness axis (downstream Hard)
//
// The lease SELF-INVALIDATES. Acquire's unlock closure is the sole writer of the
// live flag; it flips live=false (then Unlocks) when the transaction frame
// returns. A lease carried in a context that escapes its RunInTx closure
// therefore reports Live==false afterwards — the repository falls back to
// per-call locking (fail-closed), so a stale ctx can no longer skip the lock with
// no lock held. This mirrors database/sql's *Tx.done → ErrTxDone invalidation on
// commit/rollback. Lease has no Release method and no way to write live from
// outside the unlock closure, so a ctx-carried lease can neither unlock nor
// re-arm itself.
//
// The double internal/ path (cells/accesscore/internal/mem/internal/txlock)
// additionally restricts importers to the mem package tree.
package txlock

import (
	"sync"
	"sync/atomic"
)

// Lease is an un-forgeable, self-invalidating proof that Acquire locked a
// specific *sync.Mutex and that the lock is STILL held (live). It has no Release
// method by design: a Lease carried in a context can prove the lock (Live) but
// CANNOT unlock it, so a stray release mid-transaction is structurally
// impossible. The unlock capability is a separate closure returned by Acquire and
// kept local to the acquiring frame; that closure is also the sole writer of the
// live flag.
//
// The zero Lease (and any txlock.Lease{} built outside this package) holds no
// lock: Live reports false (mu == nil, live == nil). Lease is a value type
// carrying two pointers (*sync.Mutex and *atomic.Bool), so copying it copies the
// pointers (no sync.Mutex or atomic value is copied; go vet copylocks is
// satisfied). The *atomic.Bool MUST stay a pointer, not a value field: a value
// atomic.Bool would (a) trip copylocks and (b) break visibility, since the unlock
// closure and every context-carried copy of the Lease must observe the same flag.
type Lease struct {
	mu   *sync.Mutex
	live *atomic.Bool
}

// Acquire locks mu, arms a fresh live flag, and returns a Lease proof plus the
// unlock function. It is the sole constructor of a live, lock-holding Lease. The
// caller keeps unlock local and defers it (`lease, unlock := Acquire(&mu); defer
// unlock()`), then carries lease in context for inLiveTx to consult. The unlock
// closure flips the lease dead (live=false) BEFORE Unlocking, so any
// context-carried copy that outlives the frame reports Live==false. Splitting the
// proof from the unlock/invalidate capability means a context-carried Lease can
// neither release the lock nor keep itself live. MEM-TX-LOCK-OWNERSHIP-01 W1
// funnels all Acquire calls to the single sanctioned site so the
// lock/unlock/invalidate triple is local and auditable. (Same proof-vs-capability
// split as context.WithCancel returning a cancel func; same
// self-invalidation-on-completion as database/sql *Tx.done → ErrTxDone.)
func Acquire(mu *sync.Mutex) (lease Lease, unlock func()) {
	mu.Lock()
	live := &atomic.Bool{}
	live.Store(true)
	return Lease{mu: mu, live: live}, func() {
		live.Store(false)
		mu.Unlock()
	}
}

// Live reports whether l proves mu is held RIGHT NOW: pointer identity against
// the mutex Acquire locked AND the live flag still set. A lease minted for store
// A's mutex reports false for store B's mutex (cross-store safety); the zero
// Lease reports false for any mutex (live == nil); and a lease whose unlock has
// run reports false (self-invalidation, #972). The mu != nil / live != nil guards
// preceding the loads make the zero Lease and Live(nil) both safe (no nil deref).
func (l Lease) Live(mu *sync.Mutex) bool {
	return l.mu != nil && l.mu == mu && l.live != nil && l.live.Load()
}
