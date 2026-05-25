//go:build archtest_fixture

// Package txlock mirrors the production
// cells/accesscore/internal/mem/internal/txlock sealed lock-lease package so the
// MEM-TX-LOCK-OWNERSHIP-01 reverse self-check can exercise the witness-funnel
// detector (W1 Acquire-call-site + arg + nesting, W2 inLiveTx form) against a
// real, build-tag-gated source corpus. Acquire is the sole lease mint; the
// detector matches it by package name "txlock" + func name "Acquire", so this
// fixture mirror and the production package are recognized identically.
package txlock

import (
	"sync"
	"sync/atomic"
)

// Lease mirrors the production self-invalidating lease: a *sync.Mutex + a live
// *atomic.Bool behind unexported fields, un-forgeable outside this package, with
// no Release method.
type Lease struct {
	mu   *sync.Mutex
	live *atomic.Bool
}

// Acquire is the lease mint (mirrors production: proof + unlock closure that
// flips the lease dead before unlocking).
func Acquire(mu *sync.Mutex) (lease Lease, unlock func()) {
	mu.Lock()
	live := &atomic.Bool{}
	live.Store(true)
	return Lease{mu: mu, live: live}, func() {
		live.Store(false)
		mu.Unlock()
	}
}

// Live reports whether l proves mu is held right now (pointer identity + live).
func (l Lease) Live(mu *sync.Mutex) bool {
	return l.mu != nil && l.mu == mu && l.live != nil && l.live.Load()
}
