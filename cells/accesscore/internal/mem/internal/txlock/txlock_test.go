package txlock

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// TestAcquireHoldsThenUnlock verifies the witness lifecycle: Acquire locks and
// yields a Held proving that mutex; Holds is pointer-identity (true for the
// locked mutex, false for a foreign one); the returned unlock releases so the
// mutex can be re-acquired (proving Acquire really Lock()ed and unlock really
// Unlock()ed).
func TestAcquireHoldsThenUnlock(t *testing.T) {
	var mu, other sync.Mutex

	held, unlock := Acquire(&mu)
	if !held.Holds(&mu) {
		t.Fatal("Acquire(&mu) proof.Holds(&mu) = false, want true")
	}
	if held.Holds(&other) {
		t.Error("Holds(&other) = true, want false (cross-mutex identity)")
	}
	unlock()

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

// TestZeroHeldHoldsNothing verifies the un-forgeable property's runtime face:
// the zero Held (what any txlock.Held{} built outside this package collapses to)
// proves no mutex is held.
func TestZeroHeldHoldsNothing(t *testing.T) {
	var mu sync.Mutex
	var zero Held

	if zero.Holds(&mu) {
		t.Error("zero Held Holds(&mu) = true, want false")
	}
	if zero.Holds(nil) {
		t.Error("zero Held Holds(nil) = true, want false")
	}
}

// TestHeldSealFrozen is the MEM-TX-LOCK-OWNERSHIP-01 W3 seal regression guard
// (reflect field-set freeze, mirroring FixtureOpts in tools/archtest/pass_test.go).
// The Hard property — no package can forge a lock-holding Held — rests on the
// sole field being unexported and of pointer-to-mutex type. Exporting it, adding
// a field, or changing the type would weaken the seal; this fails loudly then.
func TestHeldSealFrozen(t *testing.T) {
	rt := reflect.TypeOf(Held{})
	if rt.NumField() != 1 {
		t.Fatalf("Held has %d fields, want exactly 1 (mu *sync.Mutex); adding a "+
			"field risks an exported forge path that defeats the witness seal",
			rt.NumField())
	}
	f := rt.Field(0)
	if f.Name != "mu" {
		t.Errorf("Held field[0] name = %q, want %q", f.Name, "mu")
	}
	if f.PkgPath == "" {
		t.Error("Held field[0] is exported; the witness field MUST stay unexported " +
			"so txlock.Held{<field>: …} is inexpressible outside package txlock")
	}
	if f.Type != reflect.TypeOf((*sync.Mutex)(nil)) {
		t.Errorf("Held field[0] type = %v, want *sync.Mutex", f.Type)
	}
}
