package idempotency_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	idemhttp "github.com/ghbvf/gocell/framework/runtime/http/idempotency"
	"github.com/ghbvf/gocell/framework/runtime/http/idempotency/idempotencytest"
)

// fakeClockAdvancer wraps a *clockmock.FakeClock to implement
// [idempotencytest.TimeAdvancer]. AdvancePast advances by d plus a small
// margin so any TTL of exactly d has already expired.
type fakeClockAdvancer struct {
	clk *clockmock.FakeClock
}

func (a *fakeClockAdvancer) AdvancePast(d time.Duration) {
	a.clk.Advance(d + time.Millisecond)
}

// TestMemStoreConformance runs the full idempotencytest conformance suite
// against [NewMemStore]. It enrolls MemStore into the shared backend-agnostic
// contract verified by every Store implementation.
func TestMemStoreConformance(t *testing.T) {
	factory := func(t *testing.T) (idemhttp.Store, idempotencytest.TimeAdvancer, func()) {
		t.Helper()
		clk := clockmock.New(time.Now())
		store := idemhttp.NewMemStore(clk)
		return store, &fakeClockAdvancer{clk: clk}, func() {}
	}
	idempotencytest.RunConformanceSuite(t, factory)
}
