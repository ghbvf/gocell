package healthz

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	khealthz "github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/observability/healthz/healthztest"
)

const (
	// testDeadlineCustom is the custom deadline used in TestWithDeadline_CustomValue.
	testDeadlineCustom = testtime.D2s
	// testDeadlineFast is the short deadline used in TestEvaluate_DeadlineExceededProbe.
	testDeadlineFast = testtime.D10ms
	// testProbeDelay is the clock-advance used in TestEvaluate_ProbeLatencyRecorded.
	testProbeDelay = testtime.D50ms
	// testProbeDeadlineLong is the long deadline used in latency and timeout tests.
	testProbeDeadlineLong = testtime.D5s
)

// TestRunAggregatorConformance runs the shared contract harness against the
// default in-memory aggregator.
func TestRunAggregatorConformance(t *testing.T) {
	healthztest.RunAggregatorConformance(t, func() khealthz.Aggregator {
		return NewAggregator(clock.Real())
	})
}

// --- Implementation-specific tests (not part of the shared conformance harness) ---

func TestNewAggregator_DefaultDeadline(t *testing.T) {
	agg := NewAggregator(clock.Real()).(*aggregator)
	if agg.deadline != defaultDeadline {
		t.Errorf("default deadline = %s, want %s", agg.deadline, defaultDeadline)
	}
}

func TestWithDeadline_CustomValue(t *testing.T) {
	want := testDeadlineCustom
	agg := NewAggregator(clock.Real(), WithDeadline(want)).(*aggregator)
	if agg.deadline != want {
		t.Errorf("deadline = %s, want %s", agg.deadline, want)
	}
}

func TestWithClock_UsesInjectedClock(t *testing.T) {
	clk := clockmock.New(time.Time{})
	agg := NewAggregator(clk).(*aggregator)
	if agg.clk != clk {
		t.Error("aggregator did not use injected clock")
	}
}

func TestNewAggregator_NilClockPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic on nil clock, got none")
		}
	}()
	// Provide a typed-nil clock.
	var clk *clockmock.FakeClock
	NewAggregator(clk)
}

func TestNewAggregator_ZeroDeadlinePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic on zero deadline, got none")
		}
	}()
	NewAggregator(clock.Real(), WithDeadline(0))
}

func TestEvaluate_DeadlineExceededProbe(t *testing.T) {
	agg := NewAggregator(clock.Real(), WithDeadline(testDeadlineFast))
	p := khealthz.NewProbe("slow_ready", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err := agg.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	snap := agg.Evaluate(context.Background())
	if snap.Overall != khealthz.StatusDown {
		t.Errorf("Overall = %s, want Down", snap.Overall)
	}
	if snap.Probes[0].Err == nil {
		t.Error("expected non-nil Err for timed-out probe")
	}
}

func TestEvaluate_DegradedSentinelMapsToStatusDegraded(t *testing.T) {
	agg := NewAggregator(clock.Real())
	p := khealthz.NewProbe("cache_ready", func(_ context.Context) error {
		return outbox.ErrDegraded
	})
	if err := agg.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	snap := agg.Evaluate(context.Background())
	if snap.Overall != khealthz.StatusDegraded {
		t.Errorf("Overall = %s, want Degraded", snap.Overall)
	}
	pr := snap.Probes[0]
	if pr.Status != khealthz.StatusDegraded {
		t.Errorf("probe status = %s, want Degraded", pr.Status)
	}
	if !errors.Is(pr.Err, outbox.ErrDegraded) {
		t.Errorf("probe Err should wrap ErrDegraded, got %v", pr.Err)
	}
}

func TestEvaluate_WrappedDegradedSentinel(t *testing.T) {
	agg := NewAggregator(clock.Real())
	p := khealthz.NewProbe("cache_ready", func(_ context.Context) error {
		return errors.Join(errors.New("outer"), outbox.ErrDegraded)
	})
	if err := agg.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	snap := agg.Evaluate(context.Background())
	if snap.Overall != khealthz.StatusDegraded {
		t.Errorf("Overall = %s, want Degraded (wrapped ErrDegraded)", snap.Overall)
	}
}

func TestEvaluate_ProbeLatencyRecorded(t *testing.T) {
	clk := clockmock.New(time.Unix(0, 0))
	delay := testProbeDelay

	agg := NewAggregator(clk, WithDeadline(testProbeDeadlineLong)).(*aggregator)

	// A probe that advances the fake clock so Latency is non-zero.
	p := khealthz.NewProbe("slow_ready", func(_ context.Context) error {
		clk.Advance(delay)
		return nil
	})
	if err := agg.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	snap := agg.Evaluate(context.Background())
	if snap.Probes[0].Latency < delay {
		t.Errorf("Latency = %s, want >= %s", snap.Probes[0].Latency, delay)
	}
}

func TestEvaluate_EmptyProbesSliceNotNil(t *testing.T) {
	agg := NewAggregator(clock.Real())
	snap := agg.Evaluate(context.Background())
	if snap.Probes == nil {
		t.Error("Probes should be non-nil empty slice, got nil")
	}
}

func TestEvaluate_MultipleProbesAllStatus(t *testing.T) {
	tests := []struct {
		name        string
		probes      map[string]error
		wantOverall khealthz.Status
	}{
		{
			name: "all_up",
			probes: map[string]error{
				"a_ready": nil,
				"b_ready": nil,
			},
			wantOverall: khealthz.StatusUp,
		},
		{
			name: "one_degraded_rest_up",
			probes: map[string]error{
				"a_ready": nil,
				"b_ready": outbox.ErrDegraded,
			},
			wantOverall: khealthz.StatusDegraded,
		},
		{
			name: "one_down_one_degraded",
			probes: map[string]error{
				"a_ready": errors.New("down"),
				"b_ready": outbox.ErrDegraded,
			},
			wantOverall: khealthz.StatusDown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agg := NewAggregator(clock.Real())
			for name, err := range tc.probes {
				capturedErr := err
				if regErr := agg.Register(khealthz.NewProbe(name, func(_ context.Context) error {
					return capturedErr
				})); regErr != nil {
					t.Fatalf("Register %s: %v", name, regErr)
				}
			}
			snap := agg.Evaluate(context.Background())
			if snap.Overall != tc.wantOverall {
				t.Errorf("Overall = %s, want %s", snap.Overall, tc.wantOverall)
			}
		})
	}
}

func TestCtxSafeProbe_CanceledCtxReturnsCtxErr(t *testing.T) {
	clk := clockmock.New(time.Time{})
	// Create a probe that would block forever without ctx-safe wrapping.
	inner := khealthz.NewProbe("block_ready", func(ctx context.Context) error {
		// This cooperates with ctx but simulates a slow operation.
		<-ctx.Done()
		return ctx.Err()
	})
	wrapped := wrapProbeCtxSafe(inner, clk)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately canceled

	err := wrapped.Check(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Check returned %v, want context.Canceled", err)
	}
}

func TestRunOneProbe_PanicIsStatusDown(t *testing.T) {
	agg := NewAggregator(clock.Real()).(*aggregator)
	p := khealthz.NewProbe("crash_ready", func(_ context.Context) error {
		panic("intentional panic")
	})
	// Use a non-ctx-safe probe directly in runOneProbe to exercise that path.
	ctx, cancel := context.WithTimeout(context.Background(), testProbeDeadlineLong)
	defer cancel()
	pr := agg.runOneProbe(ctx, p)
	if pr.Status != khealthz.StatusDown {
		t.Errorf("status = %s, want Down", pr.Status)
	}
	if pr.Err == nil {
		t.Error("Err should not be nil after panic")
	}
}

func TestEvaluate_SortedByName(t *testing.T) {
	agg := NewAggregator(clock.Real())
	names := []string{"z_ready", "m_ready", "a_ready", "k_ready"}
	for _, n := range names {
		n := n
		if err := agg.Register(khealthz.NewProbe(n, func(_ context.Context) error { return nil })); err != nil {
			t.Fatalf("Register %s: %v", n, err)
		}
	}
	snap := agg.Evaluate(context.Background())
	if len(snap.Probes) != len(names) {
		t.Fatalf("Probes len = %d, want %d", len(snap.Probes), len(names))
	}
	for i := 1; i < len(snap.Probes); i++ {
		if snap.Probes[i-1].Name >= snap.Probes[i].Name {
			t.Errorf("probes not sorted at index %d: %s >= %s",
				i, snap.Probes[i-1].Name, snap.Probes[i].Name)
		}
	}
}

func TestRegisterAfterDeregister_AllowsReregistration(t *testing.T) {
	agg := NewAggregator(clock.Real())
	p := khealthz.NewProbe("alpha_ready", func(_ context.Context) error { return nil })
	if err := agg.Register(p); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	agg.Deregister("alpha_ready")
	if err := agg.Register(p); err != nil {
		t.Errorf("re-register after deregister should succeed, got: %v", err)
	}
}
