package txlock

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAcquireLiveThenUnlock verifies the lease lifecycle: Acquire locks and
// yields a Lease proving that mutex is held; Live is pointer-identity AND
// liveness (true for the locked mutex while live, false for a foreign one); the
// returned unlock both releases the mutex AND invalidates the lease so Live
// reports false afterwards — the self-invalidation that closes the
// capability-escape (#972).
func TestAcquireLiveThenUnlock(t *testing.T) {
	var mu, other sync.Mutex

	lease, unlock := Acquire(&mu)
	if !lease.Live(&mu) {
		t.Fatal("Acquire(&mu) lease.Live(&mu) = false, want true")
	}
	if lease.Live(&other) {
		t.Error("Live(&other) = true, want false (cross-mutex identity)")
	}
	unlock()

	// Self-invalidation: after unlock, the lease is dead — Live(&mu) reports false
	// even though the mutex pointer still matches. An inner ctx carrying this lease
	// that escapes its RunInTx closure can therefore no longer authorize skipping
	// the per-call lock.
	if lease.Live(&mu) {
		t.Fatal("after unlock, lease.Live(&mu) = true, want false (self-invalidating lease)")
	}

	// If unlock truly released, a fresh Acquire must not block. Run it in a
	// goroutine guarded by a channel so a regression (missing Unlock) is a test
	// timeout rather than a hang.
	done := make(chan struct{})
	go func() {
		_, u2 := Acquire(&mu)
		u2()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("re-Acquire blocked: unlock did not Unlock the mutex")
	}
}

// TestZeroLeaseLiveNothing verifies the un-forgeable property's runtime face: the
// zero Lease (what any txlock.Lease{} built outside this package collapses to,
// and what the absence of a tx ctx value yields) proves no mutex is held.
func TestZeroLeaseLiveNothing(t *testing.T) {
	var mu sync.Mutex
	var zero Lease

	if zero.Live(&mu) {
		t.Error("zero Lease Live(&mu) = true, want false")
	}
	if zero.Live(nil) {
		t.Error("zero Lease Live(nil) = true, want false")
	}
}

// TestNonZeroLeaseLiveNil covers the `l.mu != nil && mu == nil` branch: a real
// live lease asked whether it holds the nil mutex must report false (no spurious
// match, no nil deref).
func TestNonZeroLeaseLiveNil(t *testing.T) {
	var mu sync.Mutex
	lease, unlock := Acquire(&mu)
	defer unlock()

	if lease.Live(nil) {
		t.Error("non-zero Lease Live(nil) = true, want false")
	}
}

// TestLeaseSealFrozen is the MEM-TX-LOCK-OWNERSHIP-01 seal regression guard
// (reflect field-set freeze, mirroring FixtureOpts in tools/archtest/pass_test.go).
// The Hard property — no package can forge a live, lock-holding Lease — rests on
// BOTH fields being unexported: mu (the locked mutex identity) and live (the
// liveness flag, written only by Acquire's unlock closure). Exporting either,
// adding a field, or changing a type would open a forge path; this fails loudly.
func TestLeaseSealFrozen(t *testing.T) {
	rt := reflect.TypeOf(Lease{})
	if rt.NumField() != 2 {
		t.Fatalf("Lease has %d fields, want exactly 2 (mu *sync.Mutex; live *atomic.Bool); "+
			"adding or exporting a field risks a forge path that defeats the lease seal",
			rt.NumField())
	}

	mu := rt.Field(0)
	if mu.Name != "mu" {
		t.Errorf("Lease field[0] name = %q, want %q", mu.Name, "mu")
	}
	if mu.PkgPath == "" {
		t.Error("Lease field[0] is exported; the mutex field MUST stay unexported " +
			"so txlock.Lease{mu: …} is inexpressible outside package txlock")
	}
	if mu.Type != reflect.TypeOf((*sync.Mutex)(nil)) {
		t.Errorf("Lease field[0] type = %v, want *sync.Mutex", mu.Type)
	}

	live := rt.Field(1)
	if live.Name != "live" {
		t.Errorf("Lease field[1] name = %q, want %q", live.Name, "live")
	}
	if live.PkgPath == "" {
		t.Error("Lease field[1] is exported; the liveness flag MUST stay unexported " +
			"so a live lease cannot be forged by setting it outside Acquire")
	}
	if live.Type != reflect.TypeOf((*atomic.Bool)(nil)) {
		t.Errorf("Lease field[1] type = %v, want *atomic.Bool", live.Type)
	}
}
