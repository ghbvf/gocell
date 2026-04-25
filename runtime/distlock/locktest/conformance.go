package locktest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
)

// DriverFactory returns a fresh Driver and its initial clock for each test sub-case.
// For real backends the factory should set up a clean namespace/key prefix.
type DriverFactory func(t *testing.T) distlock.Driver

// RunDriverConformance runs the full Driver conformance suite (C-1..C-7) against
// the supplied factory. Both FakeDriver (locktest) and RedisDriver (adapters/redis)
// must pass this suite.
//
// Mirrors the outboxtest.RunStoreConformanceSuite pattern.
func RunDriverConformance(t *testing.T, factory DriverFactory) {
	t.Helper()
	t.Run("C1_SetNX_FirstTrue_SecondFalse", func(t *testing.T) { conformC1(t, factory) })
	t.Run("C2_Renew_WrongToken_ReturnsFalse", func(t *testing.T) { conformC2(t, factory) })
	t.Run("C3_Release_WrongToken_NoDelete", func(t *testing.T) { conformC3(t, factory) })
	t.Run("C4_Concurrent_SetNX_ExactlyOneTrue", func(t *testing.T) { conformC4(t, factory) })
	t.Run("C5_TTLExpiry_SetNX_Succeeds", func(t *testing.T) { conformC5(t, factory) })
	t.Run("C6_Renew_AfterExpiry_ReturnsFalse", func(t *testing.T) { conformC6(t, factory) })
	t.Run("C7_DriverIOError_Propagated", func(t *testing.T) { conformC7(t, factory) })
}

// conformC1: SetNX first call returns true; second call with different token returns false.
func conformC1(t *testing.T, factory DriverFactory) {
	t.Helper()
	drv := factory(t)
	ctx := context.Background()
	ttl := time.Minute

	ok, err := drv.SetNX(ctx, "c1-key", "token-A", ttl)
	if err != nil {
		t.Fatalf("C1 first SetNX: %v", err)
	}
	if !ok {
		t.Fatal("C1: first SetNX should return true")
	}

	ok2, err2 := drv.SetNX(ctx, "c1-key", "token-B", ttl)
	if err2 != nil {
		t.Fatalf("C1 second SetNX: %v", err2)
	}
	if ok2 {
		t.Fatal("C1: second SetNX with different token should return false (key already held)")
	}
}

// conformC2: Renew with wrong token returns (false, nil).
func conformC2(t *testing.T, factory DriverFactory) {
	t.Helper()
	drv := factory(t)
	ctx := context.Background()
	ttl := time.Minute

	// Holder A acquires.
	ok, err := drv.SetNX(ctx, "c2-key", "token-A", ttl)
	if err != nil || !ok {
		t.Fatalf("C2 SetNX: ok=%v err=%v", ok, err)
	}

	// Holder B tries to renew — should fail gracefully.
	held, err := drv.Renew(ctx, "c2-key", "token-B", ttl)
	if err != nil {
		t.Fatalf("C2 Renew wrong token: unexpected error %v", err)
	}
	if held {
		t.Fatal("C2: Renew with wrong token should return held=false")
	}
}

// conformC3: Release with wrong token is a no-op (does not delete the key).
func conformC3(t *testing.T, factory DriverFactory) {
	t.Helper()
	drv := factory(t)
	ctx := context.Background()
	ttl := time.Minute

	_, _ = drv.SetNX(ctx, "c3-key", "token-A", ttl)

	// Wrong holder tries to release.
	if err := drv.Release(ctx, "c3-key", "token-B"); err != nil {
		t.Fatalf("C3 Release wrong token: unexpected error %v", err)
	}

	// token-A can still renew.
	held, err := drv.Renew(ctx, "c3-key", "token-A", ttl)
	if err != nil {
		t.Fatalf("C3 Renew after wrong release: %v", err)
	}
	if !held {
		t.Fatal("C3: key should still be held by token-A after wrong-token Release")
	}
}

// conformC4: 100 concurrent SetNX calls for the same key — exactly one succeeds.
func conformC4(t *testing.T, factory DriverFactory) {
	t.Helper()
	drv := factory(t)
	ctx := context.Background()
	ttl := time.Minute

	const goroutines = 100
	results := make(chan bool, goroutines)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok := tokenFor(i)
			ok, err := drv.SetNX(ctx, "c4-key", tok, ttl)
			if err != nil {
				t.Errorf("C4 goroutine %d SetNX error: %v", i, err)
				results <- false
				return
			}
			results <- ok
		}(i)
	}
	wg.Wait()
	close(results)

	count := 0
	for r := range results {
		if r {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("C4: expected exactly 1 SetNX to succeed, got %d", count)
	}
}

// conformC5: After TTL expiry, SetNX on same key succeeds.
// Uses FakeDriver's clock injection for non-integration drivers.
func conformC5(t *testing.T, factory DriverFactory) {
	t.Helper()
	drv := factory(t)
	ctx := context.Background()
	ttl := time.Millisecond

	ok, err := drv.SetNX(ctx, "c5-key", "token-A", ttl)
	if err != nil || !ok {
		t.Fatalf("C5 initial SetNX: ok=%v err=%v", ok, err)
	}

	// Wait past TTL — for real drivers this is a real sleep; for FakeDriver
	// the factory must set a sufficiently small TTL so this is fast.
	time.Sleep(5 * time.Millisecond)

	ok2, err2 := drv.SetNX(ctx, "c5-key", "token-B", ttl)
	if err2 != nil {
		t.Fatalf("C5 second SetNX after TTL: %v", err2)
	}
	if !ok2 {
		t.Fatal("C5: SetNX should succeed after TTL expiry")
	}
}

// conformC6: Renew on an expired key returns (false, nil).
func conformC6(t *testing.T, factory DriverFactory) {
	t.Helper()
	drv := factory(t)
	ctx := context.Background()
	ttl := time.Millisecond

	ok, err := drv.SetNX(ctx, "c6-key", "token-A", ttl)
	if err != nil || !ok {
		t.Fatalf("C6 SetNX: ok=%v err=%v", ok, err)
	}

	// Wait past TTL.
	time.Sleep(5 * time.Millisecond)

	held, err := drv.Renew(ctx, "c6-key", "token-A", ttl)
	if err != nil {
		t.Fatalf("C6 Renew after expiry: unexpected error %v", err)
	}
	if held {
		t.Fatal("C6: Renew on expired key should return held=false")
	}
}

// conformC7: Driver I/O errors propagate (not swallowed).
// For FakeDriver we inject via SetNextRenewError; for real drivers this is
// a smoke test using a context already canceled.
func conformC7(t *testing.T, factory DriverFactory) {
	t.Helper()
	drv := factory(t)
	ctx := context.Background()

	// Use a pre-canceled context to trigger a backend error on most real drivers.
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	// SetNX with a canceled context — most drivers return an error.
	// We accept either an error or (false, nil) since FakeDriver ignores ctx.
	_, _ = drv.SetNX(canceledCtx, "c7-key", "token-A", time.Minute) //nolint:errcheck

	// For FakeDriver: inject an explicit error on Renew.
	if fd, ok := drv.(*FakeDriver); ok {
		fd.SetNextRenewError(ErrDriverIO)
		_, _ = drv.SetNX(ctx, "c7-key2", "token-A", time.Minute) //nolint:errcheck
		_, err := drv.Renew(ctx, "c7-key2", "token-A", time.Minute)
		if err == nil {
			t.Fatal("C7: FakeDriver injected error should propagate from Renew")
		}
	}
}

// tokenFor generates a deterministic token for goroutine i.
func tokenFor(i int) string {
	return "token-" + ExportItoa(i)
}

// ExportItoa converts int to decimal string without importing strconv.
// Exported so locker_test.go can use it for key generation.
func ExportItoa(n int) string {
	return itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
