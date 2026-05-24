//go:build archtest_fixture

// Package memtxlockfixture is the MEM-TX-LOCK-OWNERSHIP-01 reverse self-check
// corpus for the sealed lock-witness funnel. It mirrors
// cells/accesscore/internal/mem's witness model (memTxToken carries a
// txlock.Held; runLocked is the only sanctioned Acquire site; WithTxContext
// mints no witness; txHoldsLock proves the lock via Held.Holds). The detector
// scanMemTxLockWitness, pointed at this package, MUST report exactly the three
// RED sites below and MUST NOT flag the clean ones:
//
//   - runLocked      — CLEAN: Acquire + memTxToken literal inside the sanctioned site
//   - WithTxContext  — CLEAN: memTxToken literal at the no-witness site
//   - leakAcquire    — RED W1: txlock.Acquire called outside runLocked
//   - txHoldsLock    — RED W2: return drops the `&& tok.held.Holds(&s.mu)` conjunct
//   - leakToken      — RED R1: memTxToken composite literal outside the two sites
//
// Bypassing the reverse self-check requires editing this real source (loaded
// via packages.Load with the archtest_fixture build tag) — not a hand-crafted
// AST string.
package memtxlockfixture

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/tools/archtest/internal/memtxlockfixture/txlock"
)

type memTxKey struct{}

// Store mirrors mem.Store's mutex-bearing shape.
type Store struct{ mu sync.Mutex }

// memTxToken mirrors the witness-bearing token (no holdsLock bool).
type memTxToken struct{ held txlock.Held }

type memTxRunner struct{ s *Store }

// runLocked is the sole sanctioned holds-lock site: Acquire here is CLEAN, and
// the memTxToken literal carrying the witness is CLEAN.
func (r memTxRunner) runLocked(ctx context.Context, fn func(context.Context) error) error {
	held, unlock := txlock.Acquire(&r.s.mu) // CLEAN: Acquire inside runLocked
	defer unlock()
	return fn(context.WithValue(ctx, memTxKey{}, &memTxToken{held: held})) // CLEAN literal
}

// WithTxContext mints no witness — CLEAN memTxToken literal at the no-witness site.
func WithTxContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, memTxKey{}, &memTxToken{}) // CLEAN literal
}

// txHoldsLock RED (W2): drops the `&& tok.held.Holds(&s.mu)` conjunct, so a
// no-witness token would falsely authorize lock-skip.
func (s *Store) txHoldsLock(ctx context.Context) bool {
	tok, _ := ctx.Value(memTxKey{}).(*memTxToken)
	return tok != nil // RED W2: missing && tok.held.Holds(&s.mu)
}

// leakAcquire RED (W1): mints a holds-lock witness outside runLocked.
func leakAcquire(s *Store) {
	_, unlock := txlock.Acquire(&s.mu) // RED W1: Acquire outside runLocked
	unlock()
}

// leakToken RED (R1): constructs a memTxToken outside the two sanctioned sites.
func leakToken() *memTxToken {
	return &memTxToken{} // RED R1: literal outside runLocked / WithTxContext
}

var (
	_ = WithTxContext
	_ = leakAcquire
	_ = leakToken
	_ = (*Store).txHoldsLock
	_ = memTxRunner.runLocked
)
