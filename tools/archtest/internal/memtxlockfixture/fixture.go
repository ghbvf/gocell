//go:build archtest_fixture

// Package memtxlockfixture is the MEM-TX-LOCK-OWNERSHIP-01 reverse self-check
// corpus for the sealed lock-lease funnel. It mirrors
// cells/accesscore/internal/mem's lease model (ctx carries a txlock.Lease
// directly — no wrapper struct; runLocked is the only sanctioned Acquire site;
// inLiveTx delegates to the sealed txlock.Lease.Live). The detector
// scanMemTxLockWitness, pointed at this package, MUST report exactly the three
// RED sites below and MUST NOT flag the clean one:
//
//   - runLocked          — CLEAN: Acquire(&r.s.mu) in the direct body + Lease
//     injected into ctx
//   - leakAcquire        — RED W1: txlock.Acquire called outside runLocked
//   - leakAcquireFuncLit — RED W1: txlock.Acquire wrapped in a closure (the
//     file-wide scan must still descend into func literals)
//   - inLiveTx           — RED W2: return drops the `l.Live(&s.mu)` delegation
//
// Bypassing the reverse self-check requires editing this real source (loaded via
// packages.Load with the archtest_fixture build tag) — not a hand-crafted AST
// string.
package memtxlockfixture

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/tools/archtest/internal/memtxlockfixture/txlock"
)

type memTxKey struct{}

// Store mirrors mem.Store's mutex-bearing shape.
type Store struct{ mu sync.Mutex }

type memTxRunner struct{ s *Store }

// runLocked is the sole sanctioned mint site: Acquire(&r.s.mu) in the direct
// body is CLEAN, and the lease injected into ctx is CLEAN.
func (r memTxRunner) runLocked(ctx context.Context, fn func(context.Context) error) error {
	lease, unlock := txlock.Acquire(&r.s.mu) // CLEAN: Acquire(&r.s.mu) inside runLocked
	defer unlock()
	return fn(context.WithValue(ctx, memTxKey{}, lease))
}

// inLiveTx RED (W2): drops the `l.Live(&s.mu)` delegation, so a stale/zero lease
// would falsely authorize a lock-skip.
func (s *Store) inLiveTx(ctx context.Context) bool {
	l, _ := ctx.Value(memTxKey{}).(txlock.Lease)
	_ = l
	return true // RED W2: missing return l.Live(&s.mu)
}

// leakAcquire RED (W1): mints a lease outside runLocked.
func leakAcquire(s *Store) {
	_, unlock := txlock.Acquire(&s.mu) // RED W1: Acquire outside runLocked
	unlock()
}

// leakAcquireFuncLit RED (W1): hides Acquire in a closure. The file-wide scan
// must descend into func literals and still flag it (it is not the sanctioned
// runLocked direct-body call).
func leakAcquireFuncLit(s *Store) {
	f := func() {
		_, unlock := txlock.Acquire(&s.mu) // RED W1: Acquire in a closure, outside runLocked
		unlock()
	}
	f()
}

var (
	_ = leakAcquire
	_ = leakAcquireFuncLit
	_ = (*Store).inLiveTx
	_ = memTxRunner.runLocked
)
