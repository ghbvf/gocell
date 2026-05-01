package locktest_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

// TestFakeDriver_Conformance runs the semantic correctness conformance suite
// against the in-memory FakeDriver. TTL physics (C-5, C-6) are deliberately
// excluded here because FakeDriver's TTL expiry is clock-injected and exercised
// via the manager's FakeClock-driven renew cycles, not real time.Sleep.
//
// Integration tests in adapters/redis/integration_test.go call both
// RunDriverConformance and RunDriverTTLConformance against a real Redis backend.
func TestFakeDriver_Conformance(t *testing.T) {
	// Each sub-test gets a fresh FakeDriver wired to a shared FakeClock so
	// TTL expiry is deterministic (no real-time dependency).
	fc := clockmock.New(time.Time{})
	locktest.RunDriverConformance(t, func(t *testing.T) distlock.Driver {
		return locktest.NewFakeDriverWithClock(fc.Now)
	})
}

// ctxErrDriver is a minimal distlock.Driver implementation used to exercise
// the "real driver" path in conformC7 (the else branch). It always returns
// ctx.Err() as the error for any operation, mimicking a context-deadline-aware
// backend.
type ctxErrDriver struct {
	// wrapped delegates all non-error-path calls to a FakeDriver.
	wrapped *locktest.FakeDriver
}

func (d *ctxErrDriver) SetNX(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return d.wrapped.SetNX(ctx, key, token, ttl)
}

func (d *ctxErrDriver) Renew(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return d.wrapped.Renew(ctx, key, token, ttl)
}

func (d *ctxErrDriver) Release(ctx context.Context, key, token string) error {
	return d.wrapped.Release(ctx, key, token)
}

var _ distlock.Driver = (*ctxErrDriver)(nil)

// TestConformanceC7_RealDriverPath exercises the else branch of conformC7
// (real driver path) by using ctxErrDriver, which is not *FakeDriver.
func TestConformanceC7_RealDriverPath(t *testing.T) {
	locktest.RunDriverConformance(t, func(t *testing.T) distlock.Driver {
		return &ctxErrDriver{wrapped: locktest.NewFakeDriver()}
	})
}
