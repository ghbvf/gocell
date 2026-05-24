// Package txlock mints un-forgeable lock-ownership witnesses for the mem
// accesscore store. It is the upstream+downstream Hard half of the mem tx
// lock-ownership funnel (MEM-TX-LOCK-OWNERSHIP-01; ADR
// docs/architecture/202605171846-adr-mem-tx-lock-ownership.md).
//
// The funnel exists to make the "sentinel present but no lock" flake
// (PR #558 concurrent-map-write) compile-impossible rather than archtest-caught:
// a tx-context token may authorize a repository method to skip its per-call
// store.mu lock ONLY if the lock is genuinely held on the calling goroutine.
//
// Held is the proof. Its sole field (mu) is unexported, so:
//
//   - No package outside txlock can write txlock.Held{mu: …} — the
//     composite-literal field is inaccessible. The empty literal txlock.Held{}
//     and the zero value carry mu == nil and Holds reports false for any mutex.
//   - The ONLY way to obtain a Held whose Holds(&store.mu) is true is to call
//     Acquire(&store.mu), which actually Lock()s the mutex first.
//
// Therefore "in a tx context AND holding the lock" cannot be expressed without
// having locked — not even inside package mem (mem can call Acquire, but Acquire
// locks; mem cannot forge a held witness via a struct literal). The double
// internal/ path (cells/accesscore/internal/mem/internal/txlock) additionally
// restricts importers to the mem package tree.
package txlock

import "sync"

// Held is an un-forgeable, read-only proof that Acquire locked a specific
// *sync.Mutex. It has no Release method by design: a Held carried in a context
// can prove the lock (Holds) but CANNOT unlock it, so a stray
// `tok.held.Release()` mid-transaction is structurally impossible. The unlock
// capability is a separate closure returned by Acquire and kept local to the
// acquiring frame.
//
// The zero Held (and any txlock.Held{} built outside this package) holds no
// lock: Holds reports false. Held is a value type carrying a *sync.Mutex
// pointer, so copying it copies the pointer (no sync.Mutex value is copied;
// go vet copylocks is satisfied).
type Held struct {
	mu *sync.Mutex
}

// Acquire locks mu and returns a Held proof plus the unlock function. It is the
// sole constructor of a lock-holding Held. The caller keeps unlock local and
// defers it (`held, unlock := Acquire(&mu); defer unlock()`), then carries held
// in context for txHoldsLock to consult. Splitting proof from unlock means a
// context-carried Held cannot release the lock. MEM-TX-LOCK-OWNERSHIP-01 W1
// funnels all Acquire calls to the single sanctioned site so the lock/unlock
// pairing is local and auditable. (Same proof-vs-capability split as
// context.WithCancel returning a cancel func.)
func Acquire(mu *sync.Mutex) (held Held, unlock func()) {
	mu.Lock()
	return Held{mu: mu}, mu.Unlock
}

// Holds reports whether h proves mu is held — pointer identity against the
// mutex Acquire locked. A token minted for store A's mutex reports false for
// store B's mutex (cross-store safety), and the zero Held reports false for any
// mutex.
func (h Held) Holds(mu *sync.Mutex) bool {
	return h.mu != nil && h.mu == mu
}
