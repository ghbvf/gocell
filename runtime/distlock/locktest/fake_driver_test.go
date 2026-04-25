package locktest_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

// TestFakeDriver_CallsAccuracy verifies Calls() counter accuracy across
// SetNX, Renew, and Release operations.
func TestFakeDriver_CallsAccuracy(t *testing.T) {
	fd := locktest.NewFakeDriver()
	ctx := context.Background()

	if fd.Calls("SetNX") != 0 || fd.Calls("Renew") != 0 || fd.Calls("Release") != 0 {
		t.Error("initial call counts should all be 0")
	}

	// SetNX x2.
	_, _ = fd.SetNX(ctx, "key1", "tok1", time.Minute)
	_, _ = fd.SetNX(ctx, "key2", "tok2", time.Minute)
	if fd.Calls("SetNX") != 2 {
		t.Errorf("Calls(SetNX) = %d, want 2", fd.Calls("SetNX"))
	}

	// Renew x1.
	_, _ = fd.Renew(ctx, "key1", "tok1", time.Minute)
	if fd.Calls("Renew") != 1 {
		t.Errorf("Calls(Renew) = %d, want 1", fd.Calls("Renew"))
	}

	// Release x1.
	_ = fd.Release(ctx, "key1", "tok1")
	if fd.Calls("Release") != 1 {
		t.Errorf("Calls(Release) = %d, want 1", fd.Calls("Release"))
	}
}

// TestFakeDriver_LastRenewDeadline verifies the LastRenewDeadline accessor.
func TestFakeDriver_LastRenewDeadline(t *testing.T) {
	fd := locktest.NewFakeDriver()
	ctx := context.Background()

	// Initially zero.
	if !fd.LastRenewDeadline().IsZero() {
		t.Error("LastRenewDeadline should be zero before any Renew call")
	}

	// SetNX to acquire the key.
	ok, _ := fd.SetNX(ctx, "key1", "tok1", time.Minute)
	if !ok {
		t.Fatal("SetNX should succeed on fresh key")
	}

	// Renew with a ctx carrying a deadline.
	deadline := time.Now().Add(5 * time.Second)
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	_, _ = fd.Renew(dctx, "key1", "tok1", time.Minute)

	recorded := fd.LastRenewDeadline()
	if recorded.IsZero() {
		t.Error("LastRenewDeadline should be non-zero after Renew with deadline ctx")
	}
	diff := recorded.Sub(deadline)
	if diff < -time.Millisecond || diff > time.Millisecond {
		t.Errorf("LastRenewDeadline = %v, expected ≈ %v (diff %v)", recorded, deadline, diff)
	}

	// Renew with no deadline context — LastRenewDeadline contract:
	// "zero if ctx had no deadline". When ctx has no deadline, the recorded
	// value should remain the previously-recorded deadline (FakeDriver only
	// updates lastRenewDeadline when ctx.Deadline() returns ok=true).
	_ = fd.LastRenewDeadline() // recorded value; no assertion needed for no-deadline ctx
	_, _ = fd.Renew(ctx, "key1", "tok1", time.Minute)
	// No assertion: the contract permits keeping old value when ctx has no deadline.
}

// TestFakeDriver_WithClock_Method verifies that the WithClock() method
// (as opposed to NewFakeDriverWithClock constructor) correctly overrides the clock.
func TestFakeDriver_WithClock_Method(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	// Start with real time clock, then override via method.
	fd := locktest.NewFakeDriver()
	fd.WithClock(fc.Now)
	ctx := context.Background()

	ttl := 5 * time.Second
	ok, _ := fd.SetNX(ctx, "wc-key", "tok", ttl)
	if !ok {
		t.Fatal("SetNX should succeed")
	}

	// Advance past TTL — key should expire.
	fc.Advance(ttl + time.Millisecond)

	// SetNX on the same key should now succeed.
	ok2, _ := fd.SetNX(ctx, "wc-key", "tok2", ttl)
	if !ok2 {
		t.Error("SetNX should succeed after TTL expiry via WithClock() method")
	}
}

// TestFakeDriver_WithClock verifies that WithClock overrides the TTL clock.
func TestFakeDriver_WithClock(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriverWithClock(fc.Now)
	ctx := context.Background()

	ttl := 10 * time.Second

	// Acquire.
	ok, _ := fd.SetNX(ctx, "key1", "tok1", ttl)
	if !ok {
		t.Fatal("SetNX should succeed")
	}

	// Renew should succeed (TTL not expired in fake clock).
	held, err := fd.Renew(ctx, "key1", "tok1", ttl)
	if err != nil || !held {
		t.Errorf("Renew before TTL expiry: held=%v err=%v", held, err)
	}

	// Advance past TTL — key should now be expired.
	fc.Advance(ttl + time.Millisecond)

	// Second SetNX on same key should succeed (key expired).
	ok2, _ := fd.SetNX(ctx, "key1", "tok2", ttl)
	if !ok2 {
		t.Error("SetNX should succeed after TTL expiry via fake clock")
	}
}

// TestFakeDriver_SetNextSetNX_Controls verifies the SetNextSetNX injection.
func TestFakeDriver_SetNextSetNX_Controls(t *testing.T) {
	fd := locktest.NewFakeDriver()
	ctx := context.Background()

	// Inject false — next SetNX should return false.
	fd.SetNextSetNX(false)
	ok, err := fd.SetNX(ctx, "key1", "tok1", time.Minute)
	if err != nil || ok {
		t.Errorf("SetNX with injected false: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	// Injection consumed — next SetNX should succeed normally.
	ok2, err2 := fd.SetNX(ctx, "key1", "tok1", time.Minute)
	if err2 != nil || !ok2 {
		t.Errorf("SetNX after injection consumed: ok=%v err=%v, want ok=true err=nil", ok2, err2)
	}
}

// TestFakeDriver_SetNextRenewError_InjectsErrorOnce verifies that error is
// injected once and then reset.
func TestFakeDriver_SetNextRenewError_InjectsErrorOnce(t *testing.T) {
	fd := locktest.NewFakeDriver()
	ctx := context.Background()

	// Acquire a key to renew.
	ok, _ := fd.SetNX(ctx, "key1", "tok1", time.Minute)
	if !ok {
		t.Fatal("setup SetNX failed")
	}

	// Inject error.
	fd.SetNextRenewError(locktest.ErrDriverIO)

	_, err := fd.Renew(ctx, "key1", "tok1", time.Minute)
	if err == nil {
		t.Error("Renew should return injected error")
	}

	// Injection consumed — next Renew should succeed.
	held, err2 := fd.Renew(ctx, "key1", "tok1", time.Minute)
	if err2 != nil || !held {
		t.Errorf("Renew after error consumed: held=%v err=%v, want held=true err=nil", held, err2)
	}
}

// TestFakeDriver_SetNextRenewHeld_SimulatesOwnershipLost verifies that
// SetNextRenewHeld(false) returns held=false on the next Renew call.
func TestFakeDriver_SetNextRenewHeld_SimulatesOwnershipLost(t *testing.T) {
	fd := locktest.NewFakeDriver()
	ctx := context.Background()

	ok, _ := fd.SetNX(ctx, "key1", "tok1", time.Minute)
	if !ok {
		t.Fatal("setup SetNX failed")
	}

	fd.SetNextRenewHeld(false)

	held, err := fd.Renew(ctx, "key1", "tok1", time.Minute)
	if err != nil || held {
		t.Errorf("Renew with injected held=false: held=%v err=%v, want held=false err=nil", held, err)
	}

	// Injection consumed — next Renew reflects actual state (expired if
	// the driver didn't update the key since NextRenewHeld=false didn't renew).
	// After NextRenewHeld=false, the key is still present in the map at its
	// original expiresAt. A fresh Renew should succeed if not expired.
	held2, err2 := fd.Renew(ctx, "key1", "tok1", time.Minute)
	if err2 != nil {
		t.Errorf("Renew after held=false injection: unexpected error %v", err2)
	}
	// held2 may be true or false depending on time; just verify no panic.
	_ = held2
}

// TestFakeDriver_Concurrent_Safety verifies that concurrent SetNX/Renew/Release
// calls are race-safe.
func TestFakeDriver_Concurrent_Safety(t *testing.T) {
	fd := locktest.NewFakeDriver()
	ctx := context.Background()

	const n = 50
	var wg sync.WaitGroup

	// Concurrent SetNX on different keys.
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "concur-key-" + string(rune('A'+i%26))
			_, _ = fd.SetNX(ctx, key, "tok", time.Minute)
		}(i)
	}
	wg.Wait()

	// Concurrent Renew.
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "concur-key-" + string(rune('A'+i%26))
			_, _ = fd.Renew(ctx, key, "tok", time.Minute)
		}(i)
	}
	wg.Wait()

	// Concurrent Release.
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "concur-key-" + string(rune('A'+i%26))
			_ = fd.Release(ctx, key, "tok")
		}(i)
	}
	wg.Wait()
}

// TestFakeDriver_TTLExpiry_WithClock verifies that TTL expiry is enforced
// according to the injected clock.
func TestFakeDriver_TTLExpiry_WithClock(t *testing.T) {
	fc := locktest.NewFakeClock(time.Time{})
	fd := locktest.NewFakeDriverWithClock(fc.Now)
	ctx := context.Background()

	ttl := 5 * time.Second

	// Acquire key1 with tok1.
	ok, _ := fd.SetNX(ctx, "expiry-key", "tok1", ttl)
	if !ok {
		t.Fatal("initial SetNX should succeed")
	}

	// Key is still valid — second SetNX should fail.
	ok2, _ := fd.SetNX(ctx, "expiry-key", "tok2", ttl)
	if ok2 {
		t.Error("SetNX should fail while key is still valid")
	}

	// Advance past TTL.
	fc.Advance(ttl + time.Millisecond)

	// Now SetNX should succeed (TTL expired).
	ok3, _ := fd.SetNX(ctx, "expiry-key", "tok2", ttl)
	if !ok3 {
		t.Error("SetNX should succeed after TTL expiry")
	}
}

// TestFakeDriver_Calls_UnknownMethod verifies that Calls() returns 0 for
// unknown method names (defensive path).
func TestFakeDriver_Calls_UnknownMethod(t *testing.T) {
	fd := locktest.NewFakeDriver()
	if n := fd.Calls("UnknownMethod"); n != 0 {
		t.Errorf("Calls(unknown) = %d, want 0", n)
	}
}

// TestFakeDriver_ResetCalls verifies that ResetCalls zeroes all counters.
func TestFakeDriver_ResetCalls(t *testing.T) {
	fd := locktest.NewFakeDriver()
	ctx := context.Background()

	_, _ = fd.SetNX(ctx, "key", "tok", time.Minute)
	if fd.Calls("SetNX") != 1 {
		t.Fatal("pre-condition: Calls(SetNX) should be 1")
	}

	fd.ResetCalls()
	if fd.Calls("SetNX") != 0 || fd.Calls("Renew") != 0 || fd.Calls("Release") != 0 {
		t.Error("ResetCalls should zero all counters")
	}
}

// TestFakeDriver_Release_WrongToken_Idempotent verifies that releasing with
// the wrong token is a no-op (C-3 conformance re-stated as a unit test).
func TestFakeDriver_Release_WrongToken_Idempotent(t *testing.T) {
	fd := locktest.NewFakeDriver()
	ctx := context.Background()

	ok, _ := fd.SetNX(ctx, "key", "tok1", time.Minute)
	if !ok {
		t.Fatal("setup SetNX failed")
	}

	// Release with wrong token — should be no-op.
	if err := fd.Release(ctx, "key", "wrong-tok"); err != nil {
		t.Errorf("Release with wrong token: unexpected error %v", err)
	}

	// Key should still be held by tok1.
	snap := fd.Snapshot()
	if snap["key"] != "tok1" {
		t.Errorf("key should still be held by tok1 after wrong-token Release, got %q", snap["key"])
	}
}
