//go:build archtest_fixture

// Package txlock mirrors the production
// cells/accesscore/internal/mem/internal/txlock sealed lock-witness package so
// the MEM-TX-LOCK-OWNERSHIP-01 reverse self-check can exercise the
// witness-funnel detector (W1 Acquire-call-site, W2 txHoldsLock form) against a
// real, build-tag-gated source corpus. Acquire is the sole witness mint; the
// detector matches it by package name "txlock" + func name "Acquire", so this
// fixture mirror and the production package are recognized identically.
package txlock

import "sync"

// Held mirrors the production Held: a *sync.Mutex behind an unexported field,
// un-forgeable outside this package.
type Held struct{ mu *sync.Mutex }

// Acquire is the witness mint (mirrors production).
func Acquire(mu *sync.Mutex) Held {
	mu.Lock()
	return Held{mu: mu}
}

// Release unlocks the witnessed mutex.
func (h Held) Release() {
	if h.mu != nil {
		h.mu.Unlock()
	}
}

// Holds reports whether h witnesses mu (pointer identity).
func (h Held) Holds(mu *sync.Mutex) bool {
	return h.mu != nil && h.mu == mu
}
