// Package locktest provides a controllable in-memory Driver implementation
// and a conformance test suite for use in unit tests.
//
// # Conformance suites
//
// Two suites are provided to separate concerns:
//
//   - RunDriverConformance: semantic correctness (C-1..C-4, C-7). Runs against
//     any Driver, including FakeDriver. No real-time waits; safe for -race.
//
//   - RunDriverTTLConformance: TTL physics (C-5, C-6). Uses real time.Sleep
//     because it is testing real-clock TTL expiry on backends. Intended for
//     integration tests (e.g., adapters/redis/integration_test.go) where
//     wall-clock waits are acceptable.
//
// FakeDriver conformance tests (conformance_test.go) call only
// RunDriverConformance. Redis integration tests call both.
package locktest

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/distlock"
)

// ttlExpiryMargin is the safety multiplier applied to a small TTL when waiting
// for the backend to physically expire it. The actual wait equals
// `ttl * ttlExpiryMargin`; the margin is dimensionless so changing the
// fixture TTL automatically scales the wait. 5× is empirically sufficient
// across local Redis and Redis Cluster.
//
// C-5 uses polling (SetNX is side-effect-free on a held key, so we can
// retry until the TTL has expired). C-6 must use a fixed sleep because
// Renew on a held key extends the TTL — polling Renew would never observe
// expiry. See docs/architecture/test-time-discipline.md
// "Intentional real-clock sleeps".
const ttlExpiryMargin = 5

const conformDNeg1h = -1 * time.Hour

// Literal constants for repeated token/key strings used in conformance tests.
// Per CLAUDE.md: strings repeated ≥3 times must be extracted as constants.
const (
	tokenA = "token-A"
	tokenB = "token-B"
	c3Key  = "c3-key"
)

// DriverFactory returns a fresh Driver and its initial clock for each test sub-case.
// For real backends the factory should set up a clean namespace/key prefix.
type DriverFactory func(t *testing.T) distlock.Driver

// RunDriverConformance runs the semantic correctness conformance suite
// (C-1..C-4, C-7) against the supplied factory. Both FakeDriver (locktest)
// and RedisDriver (adapters/redis) must pass this suite.
//
// This suite makes no real-time waits and is safe to run with -race.
//
// Mirrors the outboxtest.RunStoreConformanceSuite pattern.
func RunDriverConformance(t *testing.T, factory DriverFactory) {
	t.Helper()
	t.Run("C1_SetNX_FirstTrue_SecondFalse", func(t *testing.T) { conformC1(t, factory) })
	t.Run("C2_Renew_WrongToken_ReturnsFalse", func(t *testing.T) { conformC2(t, factory) })
	t.Run("C3_Release_WrongToken_NoDelete", func(t *testing.T) { conformC3(t, factory) })
	t.Run("C4_Concurrent_SetNX_ExactlyOneTrue", func(t *testing.T) { conformC4(t, factory) })
	t.Run("C7_DriverIOError_Propagated", func(t *testing.T) { conformC7(t, factory) })
}

// RunDriverTTLConformance runs the TTL physics conformance suite (C-5, C-6)
// against the supplied factory. These tests use real time.Sleep because they
// are validating actual backend TTL expiry.
//
// Call this from integration tests where wall-clock waits are acceptable
// (e.g., adapters/redis/integration_test.go). Do NOT call it for FakeDriver
// conformance — FakeDriver's TTL expiry is clock-injected and tested via
// the manager's FakeClock-driven renew cycles instead.
func RunDriverTTLConformance(t *testing.T, factory DriverFactory) {
	t.Helper()
	t.Run("C5_TTLExpiry_SetNX_Succeeds", func(t *testing.T) { conformC5(t, factory) })
	t.Run("C6_Renew_AfterExpiry_ReturnsFalse", func(t *testing.T) { conformC6(t, factory) })
}

// conformC1: SetNX first call returns true; second call with different token returns false.
func conformC1(t *testing.T, factory DriverFactory) {
	t.Helper()
	drv := factory(t)
	ctx := context.Background()
	ttl := time.Minute

	ok, err := drv.SetNX(ctx, "c1-key", tokenA, ttl)
	if err != nil {
		t.Fatalf("C1 first SetNX: %v", err)
	}
	if !ok {
		t.Fatal("C1: first SetNX should return true")
	}

	ok2, err2 := drv.SetNX(ctx, "c1-key", tokenB, ttl)
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
	ok, err := drv.SetNX(ctx, "c2-key", tokenA, ttl)
	if err != nil || !ok {
		t.Fatalf("C2 SetNX: ok=%v err=%v", ok, err)
	}

	// Holder B tries to renew — should fail gracefully.
	held, err := drv.Renew(ctx, "c2-key", tokenB, ttl)
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

	ok, err := drv.SetNX(ctx, c3Key, tokenA, ttl)
	if err != nil {
		t.Fatalf("C3 setup SetNX: %v", err)
	}
	if !ok {
		t.Fatal("C3 setup: SetNX should succeed on fresh key")
	}

	// Wrong holder tries to release.
	if err := drv.Release(ctx, c3Key, tokenB); err != nil {
		t.Fatalf("C3 Release wrong token: unexpected error %v", err)
	}

	// token-A can still renew.
	held, err := drv.Renew(ctx, c3Key, tokenA, ttl)
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
// Polls SetNX with a generous deadline because SetNX has no effect on a
// still-held key — each poll is safe and the test self-times to whatever
// the backend's actual expiry latency is.
// Only call via RunDriverTTLConformance for backends that enforce physical
// TTL (e.g. Redis). Do not call for FakeDriver tests.
func conformC5(t *testing.T, factory DriverFactory) {
	t.Helper()
	drv := factory(t)
	ctx := context.Background()
	ttl := time.Millisecond

	ok, err := drv.SetNX(ctx, "c5-key", tokenA, ttl)
	if err != nil || !ok {
		t.Fatalf("C5 initial SetNX: ok=%v err=%v", ok, err)
	}

	deadline := time.Now().Add(testtime.EventuallyShort)
	for {
		ok2, err2 := drv.SetNX(ctx, "c5-key", tokenB, ttl)
		if err2 != nil {
			t.Fatalf("C5 second SetNX: %v", err2)
		}
		if ok2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("C5: SetNX did not succeed within deadline after TTL expiry")
		}
		time.Sleep(testtime.FastPoll) //archtest:allow:test-sleep TTL physical expiry; backend has no notification API
	}
}

// conformC6: Renew on an expired key returns (false, nil).
// Uses a fixed sleep (not Eventually-style polling) because Renew on a
// still-held key extends the TTL — polling would defeat the test.
// Only call via RunDriverTTLConformance.
func conformC6(t *testing.T, factory DriverFactory) {
	t.Helper()
	drv := factory(t)
	ctx := context.Background()
	ttl := time.Millisecond

	ok, err := drv.SetNX(ctx, "c6-key", tokenA, ttl)
	if err != nil || !ok {
		t.Fatalf("C6 SetNX: ok=%v err=%v", ok, err)
	}

	// Wait ttl × ttlExpiryMargin past the deadline — Renew cannot be polled.
	time.Sleep(ttl * ttlExpiryMargin) //archtest:allow:test-sleep TTL physical expiry; backend has no notification API

	held, err := drv.Renew(ctx, "c6-key", tokenA, ttl)
	if err != nil {
		t.Fatalf("C6 Renew after expiry: unexpected error %v", err)
	}
	if held {
		t.Fatal("C6: Renew on expired key should return held=false")
	}
}

// conformC7: Driver I/O errors propagate (not swallowed).
// For FakeDriver we inject via SetNextRenewError; for real drivers we use an
// already-past-deadline context to guarantee an error path on the SetNX call.
func conformC7(t *testing.T, factory DriverFactory) {
	t.Helper()
	drv := factory(t)
	ctx := context.Background()

	// For FakeDriver: inject an explicit error on Renew to verify propagation.
	// FakeDriver ignores ctx for in-memory ops (acceptable); we test error
	// propagation explicitly via SetNextRenewError.
	if fd, ok := drv.(*FakeDriver); ok {
		fd.SetNextRenewError(ErrDriverIO)
		// SetNX with a valid context so the key is actually held.
		setOK, setErr := drv.SetNX(ctx, "c7-key2", tokenA, time.Minute)
		if setErr != nil || !setOK {
			t.Fatalf("C7 FakeDriver setup SetNX: ok=%v err=%v", setOK, setErr)
		}
		_, renewErr := drv.Renew(ctx, "c7-key2", tokenA, time.Minute)
		if renewErr == nil {
			t.Fatal("C7: FakeDriver injected error should propagate from Renew")
		}
	} else {
		// For real drivers: use an already-past-deadline context to force an error.
		// This validates that the driver faithfully propagates ctx errors.
		alreadyExpiredCtx, cancel := context.WithDeadline(ctx, time.Now().Add(conformDNeg1h))
		defer cancel()
		_, err := drv.SetNX(alreadyExpiredCtx, "c7-key", tokenA, time.Minute)
		if err == nil {
			t.Errorf("C7: real driver SetNX with already-past-deadline ctx should return an error")
		}
	}
}

// tokenFor generates a deterministic token for goroutine i.
func tokenFor(i int) string {
	return "token-" + strconv.Itoa(i)
}
