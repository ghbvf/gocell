package healthz

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	khealthz "github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// conformanceDeadlineFast is the per-probe deadline used in the
// probe_exceeding_deadline_returns_down conformance sub-test. A short value
// keeps the test fast; 20ms is sufficient for in-process goroutine scheduling.
const conformanceDeadlineFast = 20 * time.Millisecond

// RunAggregatorConformance validates that the given factory produces
// [kernel/healthz.Aggregator] implementations satisfying the full kernel
// contract. It is the single-source conformance harness; future postgres or
// otel adapters must call this function to verify their implementations.
//
// The factory is called once per sub-test case. Each sub-test is independent.
//
// Usage:
//
//	func TestMyAggregatorConformance(t *testing.T) {
//	    healthz.RunAggregatorConformance(t, func() khealthz.Aggregator {
//	        return MyNewAggregator()
//	    })
//	}
//
// splitting each into a top-level function fragments the contract narrative
// and breaks single-source-of-truth invariant: one function = full contract.
//
//nolint:gocognit,cyclop,funlen // conformance harness inlines 12 t.Run subtests by design;
func RunAggregatorConformance(t *testing.T, factory func() khealthz.Aggregator) {
	t.Helper()

	t.Run("empty_aggregator_returns_up", func(t *testing.T) {
		agg := factory()
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusUp {
			t.Errorf("empty aggregator: Overall = %s, want %s", snap.Overall, khealthz.StatusUp)
		}
		if len(snap.Probes) != 0 {
			t.Errorf("empty aggregator: Probes len = %d, want 0", len(snap.Probes))
		}
	})

	t.Run("up_probe_returns_up_snapshot", func(t *testing.T) {
		agg := factory()
		p := khealthz.NewProbe("alpha_ready", func(_ context.Context) error { return nil })
		if err := agg.Register(p); err != nil {
			t.Fatalf("Register: %v", err)
		}
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusUp {
			t.Errorf("Overall = %s, want %s", snap.Overall, khealthz.StatusUp)
		}
		if len(snap.Probes) != 1 {
			t.Fatalf("Probes len = %d, want 1", len(snap.Probes))
		}
		if snap.Probes[0].Status != khealthz.StatusUp {
			t.Errorf("probe status = %s, want %s", snap.Probes[0].Status, khealthz.StatusUp)
		}
	})

	t.Run("one_down_probe_overall_down", func(t *testing.T) {
		agg := factory()
		upProbe := khealthz.NewProbe("alpha_ready", func(_ context.Context) error { return nil })
		downProbe := khealthz.NewProbe("beta_ready", func(_ context.Context) error {
			return errors.New("db unavailable")
		})
		if err := agg.Register(upProbe); err != nil {
			t.Fatalf("Register up: %v", err)
		}
		if err := agg.Register(downProbe); err != nil {
			t.Fatalf("Register down: %v", err)
		}
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusDown {
			t.Errorf("Overall = %s, want %s", snap.Overall, khealthz.StatusDown)
		}
	})

	t.Run("down_and_degraded_overall_down", func(t *testing.T) {
		agg := factory()
		downProbe := khealthz.NewProbe("alpha_ready", func(_ context.Context) error {
			return errors.New("hard failure")
		})
		degradedProbe := khealthz.NewProbe("beta_ready", func(_ context.Context) error {
			return cell.ErrDegraded
		})
		if err := agg.Register(downProbe); err != nil {
			t.Fatalf("Register down: %v", err)
		}
		if err := agg.Register(degradedProbe); err != nil {
			t.Fatalf("Register degraded: %v", err)
		}
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusDown {
			t.Errorf("Overall = %s, want %s (down beats degraded)", snap.Overall, khealthz.StatusDown)
		}
	})

	t.Run("up_and_degraded_overall_degraded", func(t *testing.T) {
		agg := factory()
		upProbe := khealthz.NewProbe("alpha_ready", func(_ context.Context) error { return nil })
		degradedProbe := khealthz.NewProbe("beta_ready", func(_ context.Context) error {
			return cell.ErrDegraded
		})
		if err := agg.Register(upProbe); err != nil {
			t.Fatalf("Register up: %v", err)
		}
		if err := agg.Register(degradedProbe); err != nil {
			t.Fatalf("Register degraded: %v", err)
		}
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusDegraded {
			t.Errorf("Overall = %s, want %s", snap.Overall, khealthz.StatusDegraded)
		}
	})

	t.Run("duplicate_register_returns_err_duplicate_probe", func(t *testing.T) {
		agg := factory()
		p := khealthz.NewProbe("alpha_ready", func(_ context.Context) error { return nil })
		if err := agg.Register(p); err != nil {
			t.Fatalf("first Register: %v", err)
		}
		err := agg.Register(p)
		if !errors.Is(err, khealthz.ErrDuplicateProbe) {
			t.Errorf("second Register err = %v, want ErrDuplicateProbe", err)
		}
	})

	t.Run("deregister_removes_probe", func(t *testing.T) {
		agg := factory()
		downProbe := khealthz.NewProbe("alpha_ready", func(_ context.Context) error {
			return errors.New("failure")
		})
		if err := agg.Register(downProbe); err != nil {
			t.Fatalf("Register: %v", err)
		}
		// Before deregister — should be Down.
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusDown {
			t.Errorf("before deregister: Overall = %s, want Down", snap.Overall)
		}
		agg.Deregister("alpha_ready")
		// After deregister — should be Up (empty).
		snap = agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusUp {
			t.Errorf("after deregister: Overall = %s, want Up", snap.Overall)
		}
		if len(snap.Probes) != 0 {
			t.Errorf("after deregister: Probes len = %d, want 0", len(snap.Probes))
		}
	})

	t.Run("deregister_unknown_name_noop", func(t *testing.T) {
		agg := factory()
		// Must not panic or return an error.
		agg.Deregister("does_not_exist")
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusUp {
			t.Errorf("Overall = %s, want Up", snap.Overall)
		}
	})

	t.Run("panicking_probe_returns_down_with_panic_prefix", func(t *testing.T) {
		agg := factory()
		p := khealthz.NewProbe("alpha_ready", func(_ context.Context) error {
			panic(panicregister.Approved("conformance-probe-panic-recovery",
				errcode.Assertion("test panic payload")))
		})
		if err := agg.Register(p); err != nil {
			t.Fatalf("Register: %v", err)
		}
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusDown {
			t.Errorf("Overall = %s, want Down", snap.Overall)
		}
		if len(snap.Probes) == 0 {
			t.Fatal("Probes is empty")
		}
		pr := snap.Probes[0]
		if pr.Status != khealthz.StatusDown {
			t.Errorf("probe status = %s, want Down", pr.Status)
		}
		if pr.Err == nil || !strings.Contains(pr.Err.Error(), "panic:") {
			t.Errorf("probe err = %v, want message containing 'panic:'", pr.Err)
		}
	})

	t.Run("probe_exceeding_deadline_returns_down", func(t *testing.T) {
		// Use a very short deadline to avoid slow tests.
		agg := NewAggregator(WithClock(clock.Real()), WithDeadline(conformanceDeadlineFast))
		p := khealthz.NewProbe("alpha_ready", func(ctx context.Context) error {
			// Block until ctx is done (honors cancellation).
			<-ctx.Done()
			return ctx.Err()
		})
		if err := agg.Register(p); err != nil {
			t.Fatalf("Register: %v", err)
		}
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusDown {
			t.Errorf("Overall = %s, want Down (timeout)", snap.Overall)
		}
	})

	t.Run("concurrent_register_evaluate_no_race", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping concurrency test in -short mode")
		}
		agg := factory()
		const workers = 20
		var wg sync.WaitGroup
		wg.Add(workers * 2)
		for i := range workers {
			// Writers: register then deregister a uniquely named probe.
			go func(n int) {
				defer wg.Done()
				name := probeNameForWorker(n)
				p := khealthz.NewProbe(name, func(_ context.Context) error { return nil })
				_ = agg.Register(p) // ignore duplicate errors from concurrent writers
				agg.Deregister(name)
			}(i)
			// Readers: evaluate concurrently.
			go func() {
				defer wg.Done()
				agg.Evaluate(context.Background())
			}()
		}
		wg.Wait()
	})

	t.Run("probes_returned_sorted_by_name", func(t *testing.T) {
		agg := factory()
		names := []string{"zeta_ready", "alpha_ready", "beta_ready"}
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
				t.Errorf("Probes not sorted: %s >= %s", snap.Probes[i-1].Name, snap.Probes[i].Name)
			}
		}
	})
}

// probeNameForWorker returns a unique probe name for use in concurrency tests.
// It avoids package-level state; the name just needs to be unique per worker.
func probeNameForWorker(n int) string {
	const digits = "0123456789"
	// Simple: "probe_N" rendered without fmt to avoid fmt import in conformance.
	s := []byte("probe_000")
	s[8] = digits[n%10]
	s[7] = digits[(n/10)%10]
	s[6] = digits[(n/100)%10]
	return string(s)
}
