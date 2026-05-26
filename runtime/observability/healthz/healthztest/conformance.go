// Package healthztest provides shared test helpers for [kernel/healthz.Aggregator]
// implementations. It is a test-only package (not imported in production paths)
// and lives in a subpackage to avoid pulling the "testing" import into the
// production runtime/observability/healthz surface.
package healthztest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	khealthz "github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// Shared assertion-message formats — these literals recur across multiple
// conformance sub-tests (extracted per go:S1192).
const (
	msgOverallWant = "Overall = %s, want %s"
	msgRegisterErr = "Register: %v"
)

// RunAggregatorConformance validates that the given factory produces
// [kernel/healthz.Aggregator] implementations satisfying the full kernel
// contract. It is the single-source conformance harness; future postgres or
// otel adapters must call this function to verify their implementations.
//
// The factory is called once per sub-test case. Each sub-test is independent.
//
// The "probe_exceeding_deadline_returns_down" sub-test is intentionally omitted
// from the generic harness because the [kernel/healthz.Aggregator] interface does
// not expose deadline configuration. Implementations that support configurable
// deadlines must test that behavior in their own implementation-specific tests
// (e.g., runtime/observability/healthz.TestEvaluate_DeadlineExceededProbe).
//
// Usage:
//
//	func TestMyAggregatorConformance(t *testing.T) {
//	    healthztest.RunAggregatorConformance(t, func() khealthz.Aggregator {
//	        return MyNewAggregator()
//	    })
//	}
//
// splitting each into a top-level function fragments the contract narrative
// and breaks single-source-of-truth invariant: one function = full contract.
//
//nolint:gocognit,cyclop,funlen // conformance harness inlines 13 t.Run subtests by design;
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
		p := khealthz.NewProbe(khealthz.MustProbeName("alpha_ready"), func(_ context.Context) error { return nil })
		if err := agg.Register(p); err != nil {
			t.Fatalf(msgRegisterErr, err)
		}
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusUp {
			t.Errorf(msgOverallWant, snap.Overall, khealthz.StatusUp)
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
		upProbe := khealthz.NewProbe(khealthz.MustProbeName("alpha_ready"), func(_ context.Context) error { return nil })
		downProbe := khealthz.NewProbe(khealthz.MustProbeName("beta_ready"), func(_ context.Context) error {
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
			t.Errorf(msgOverallWant, snap.Overall, khealthz.StatusDown)
		}
	})

	t.Run("down_and_degraded_overall_down", func(t *testing.T) {
		agg := factory()
		downProbe := khealthz.NewProbe(khealthz.MustProbeName("alpha_ready"), func(_ context.Context) error {
			return errors.New("hard failure")
		})
		degradedProbe := khealthz.NewProbe(khealthz.MustProbeName("beta_ready"), func(_ context.Context) error {
			return outbox.ErrDegraded
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
		upProbe := khealthz.NewProbe(khealthz.MustProbeName("alpha_ready"), func(_ context.Context) error { return nil })
		degradedProbe := khealthz.NewProbe(khealthz.MustProbeName("beta_ready"), func(_ context.Context) error {
			return outbox.ErrDegraded
		})
		if err := agg.Register(upProbe); err != nil {
			t.Fatalf("Register up: %v", err)
		}
		if err := agg.Register(degradedProbe); err != nil {
			t.Fatalf("Register degraded: %v", err)
		}
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusDegraded {
			t.Errorf(msgOverallWant, snap.Overall, khealthz.StatusDegraded)
		}
	})

	t.Run("duplicate_register_returns_err_duplicate_probe", func(t *testing.T) {
		agg := factory()
		p := khealthz.NewProbe(khealthz.MustProbeName("alpha_ready"), func(_ context.Context) error { return nil })
		if err := agg.Register(p); err != nil {
			t.Fatalf("first Register: %v", err)
		}
		err := agg.Register(p)
		if !errors.Is(err, khealthz.ErrDuplicateProbe) {
			t.Errorf("second Register err = %v, want ErrDuplicateProbe", err)
		}
	})

	t.Run("nil_probe_returns_invalid_probe_name", func(t *testing.T) {
		agg := factory()
		err := agg.Register(nil)
		if !errors.Is(err, khealthz.ErrInvalidProbeName) {
			t.Errorf("Register(nil) err = %v, want ErrInvalidProbeName", err)
		}
	})

	t.Run("empty_name_returns_invalid_probe_name", func(t *testing.T) {
		agg := factory()
		err := agg.Register(emptyNameProbe{})
		if !errors.Is(err, khealthz.ErrInvalidProbeName) {
			t.Errorf("Register(empty-name probe) err = %v, want ErrInvalidProbeName", err)
		}
		// The rejected probe must not pollute the snapshot.
		snap := agg.Evaluate(context.Background())
		if len(snap.Probes) != 0 {
			t.Errorf("after rejected Register: Probes len = %d, want 0", len(snap.Probes))
		}
	})

	t.Run("deregister_removes_probe", func(t *testing.T) {
		agg := factory()
		downProbe := khealthz.NewProbe(khealthz.MustProbeName("alpha_ready"), func(_ context.Context) error {
			return errors.New("failure")
		})
		if err := agg.Register(downProbe); err != nil {
			t.Fatalf(msgRegisterErr, err)
		}
		// Before deregister — should be Down.
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusDown {
			t.Errorf("before deregister: Overall = %s, want Down", snap.Overall)
		}
		agg.Deregister(khealthz.MustProbeName("alpha_ready"))
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
		agg.Deregister(khealthz.MustProbeName("does_not_exist"))
		snap := agg.Evaluate(context.Background())
		if snap.Overall != khealthz.StatusUp {
			t.Errorf("Overall = %s, want Up", snap.Overall)
		}
	})

	t.Run("panicking_probe_returns_down_with_panic_prefix", func(t *testing.T) {
		agg := factory()
		p := khealthz.NewProbe(khealthz.MustProbeName("alpha_ready"), func(_ context.Context) error {
			panic(panicregister.Approved("conformance-probe-panic-recovery",
				errcode.Assertion("test panic payload")))
		})
		if err := agg.Register(p); err != nil {
			t.Fatalf(msgRegisterErr, err)
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
			if err := agg.Register(khealthz.NewProbe(khealthz.MustProbeName(n), func(_ context.Context) error { return nil })); err != nil {
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

// emptyNameProbe is a deliberately malformed Probe whose Name() is empty. It
// cannot be built via khealthz.NewProbe (which panics on an empty name), so the
// conformance harness constructs it directly to exercise the Aggregator's
// registration-time name validation (ErrInvalidProbeName).
type emptyNameProbe struct{}

func (emptyNameProbe) Name() khealthz.ProbeName      { return "" }
func (emptyNameProbe) Check(_ context.Context) error { return nil }

// probeNameForWorker returns a unique probe name for use in concurrency tests.
// It avoids package-level state; the name just needs to be unique per worker.
func probeNameForWorker(n int) khealthz.ProbeName {
	const digits = "0123456789"
	// Simple: "probe_N" rendered without fmt to avoid fmt import in conformance.
	s := []byte("probe_000")
	s[8] = digits[n%10]
	s[7] = digits[(n/10)%10]
	s[6] = digits[(n/100)%10]
	return khealthz.MustProbeName(string(s))
}

// NewFakeAggregator returns a [*FakeAggregator] that implements
// [kernel/healthz.Aggregator]. It stores registered probes by name and
// runs them on Evaluate. This shared helper eliminates the 5 duplicate
// testAggregator definitions across cells/ and example cells.
//
// Use in tests that need a real aggregator but do not require the full
// runtime/observability/healthz implementation (e.g. cell unit tests that
// verify a probe is registered with the correct name).
//
// The returned concrete type exposes [FakeAggregator.Probe] for tests that
// need to inspect individual probe check functions by name.
func NewFakeAggregator() *FakeAggregator {
	return &FakeAggregator{probes: make(map[khealthz.ProbeName]khealthz.Probe)}
}

// FakeAggregator is a lightweight [kernel/healthz.Aggregator] stub for tests.
// It is exported so test files in cells/ can access the Probe method without
// requiring a type assertion.
type FakeAggregator struct {
	mu     sync.RWMutex
	probes map[khealthz.ProbeName]khealthz.Probe
}

// Register implements [kernel/healthz.Aggregator].
func (a *FakeAggregator) Register(p khealthz.Probe) error {
	if p == nil {
		return fmt.Errorf("%w: nil probe", khealthz.ErrInvalidProbeName)
	}
	if p.Name() == "" {
		return fmt.Errorf("%w: empty probe name", khealthz.ErrInvalidProbeName)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, dup := a.probes[p.Name()]; dup {
		return fmt.Errorf("%w: probe %q", khealthz.ErrDuplicateProbe, p.Name())
	}
	a.probes[p.Name()] = p
	return nil
}

// Deregister implements [kernel/healthz.Aggregator].
func (a *FakeAggregator) Deregister(name khealthz.ProbeName) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.probes, name)
}

// Evaluate implements [kernel/healthz.Aggregator]. It runs all registered
// probe Check functions and folds results.
func (a *FakeAggregator) Evaluate(ctx context.Context) khealthz.Snapshot {
	a.mu.RLock()
	probes := make([]khealthz.Probe, 0, len(a.probes))
	for _, p := range a.probes {
		probes = append(probes, p)
	}
	a.mu.RUnlock()

	results := make([]khealthz.ProbeResult, 0, len(probes))
	overall := khealthz.StatusUp
	for _, p := range probes {
		var pr khealthz.ProbeResult
		pr.Name = p.Name()
		pr.Err = p.Check(ctx)
		switch {
		case pr.Err == nil:
			pr.Status = khealthz.StatusUp
		case errors.Is(pr.Err, outbox.ErrDegraded):
			pr.Status = khealthz.StatusDegraded
		default:
			pr.Status = khealthz.StatusDown
		}
		overall = khealthz.WorseStatus(overall, pr.Status)
		results = append(results, pr)
	}
	// Sort by name for deterministic output — matches production aggregator behavior
	// (runtime/observability/healthz.aggregator.Evaluate sorts before returning).
	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})
	return khealthz.Snapshot{Overall: overall, Probes: results}
}

// Probe returns the registered probe for the given name, or nil if not found.
// This is useful in tests that need to call the probe's Check function directly.
func (a *FakeAggregator) Probe(name khealthz.ProbeName) khealthz.Probe {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.probes[name]
}

// HasProbe reports whether a probe with the given name is registered.
func (a *FakeAggregator) HasProbe(name khealthz.ProbeName) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.probes[name]
	return ok
}

// ProbeNames returns the names of all registered probes. Order is not guaranteed.
func (a *FakeAggregator) ProbeNames() []khealthz.ProbeName {
	a.mu.RLock()
	defer a.mu.RUnlock()
	names := make([]khealthz.ProbeName, 0, len(a.probes))
	for k := range a.probes {
		names = append(names, k)
	}
	return names
}
