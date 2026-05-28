package metrics_test

import (
	"context"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/saga/executor"
)

// Compile-time check: SagaStepCollector must implement executor.Observer.
var _ executor.Observer = (*obmetrics.SagaStepCollector)(nil)

// Compile-time check: sagaSpyProvider must implement kernelmetrics.Provider so
// that any future Provider interface expansion is caught at compile time.
var _ kernelmetrics.Provider = (*sagaSpyProvider)(nil)

// TestNewSagaStepCollector_RejectsNilProvider asserts fail-fast on nil.
func TestNewSagaStepCollector_RejectsNilProvider(t *testing.T) {
	_, err := obmetrics.NewSagaStepCollector(nil, "accesscore")
	if err == nil {
		t.Fatal("expected error for nil provider")
	}
}

// TestNewSagaStepCollector_RejectsEmptyCellID asserts fail-fast on empty cell.
// SagaCollector is per-cell — no _runtime fallback.
func TestNewSagaStepCollector_RejectsEmptyCellID(t *testing.T) {
	_, err := obmetrics.NewSagaStepCollector(kernelmetrics.NopProvider{}, "")
	if err == nil {
		t.Fatal("expected error for empty cellID")
	}
}

// TestNewSagaStepCollector_RegistersThreeCounters asserts the three saga
// counters are registered (and no others).
func TestNewSagaStepCollector_RegistersThreeCounters(t *testing.T) {
	p := newSagaSpyProvider()
	_, err := obmetrics.NewSagaStepCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaStepCollector: %v", err)
	}

	wantCounters := []string{
		"saga_step_outcome_total",
		"saga_step_retry_total",
		"saga_heartbeat_failed_total",
	}
	for _, name := range wantCounters {
		if _, ok := p.counterNames[name]; !ok {
			t.Errorf("SagaStepCollector did not register %q", name)
		}
	}
	if len(p.counterNames) != len(wantCounters) {
		t.Errorf("counter count = %d, want %d (extras: %v)",
			len(p.counterNames), len(wantCounters), p.counterNames)
	}
	if len(p.gaugeNames) != 0 {
		t.Errorf("SagaStepCollector must not register gauges, got %v", p.gaugeNames)
	}
}

// TestSagaStepCollector_ObserveOutcome_AllFiveOutcomes verifies wire-stable
// outcome labels for the five Outcome variants.
func TestSagaStepCollector_ObserveOutcome_AllFiveOutcomes(t *testing.T) {
	cases := []struct {
		outcome executor.Outcome
		want    string
	}{
		{executor.OutcomeSucceeded, "succeeded"},
		{executor.OutcomeFailed, "failed"},
		{executor.OutcomeExpired, "expired"},
		{executor.OutcomeCanceled, "canceled"},
		{executor.OutcomeLeaseLost, "lease_lost"},
	}

	p := newSagaSpyProvider()
	c, err := obmetrics.NewSagaStepCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaStepCollector: %v", err)
	}
	for _, tc := range cases {
		c.ObserveOutcome(context.Background(), "def-x", "step-1", tc.outcome, 1)
	}

	ops := p.counterOps["saga_step_outcome_total"]
	if len(ops) != len(cases) {
		t.Fatalf("counter ops = %d, want %d", len(ops), len(cases))
	}
	for i, tc := range cases {
		if got := ops[i].labels["outcome"]; got != tc.want {
			t.Errorf("ops[%d].outcome = %q, want %q", i, got, tc.want)
		}
		if got := ops[i].labels["cell"]; got != "auditcore" {
			t.Errorf("ops[%d].cell = %q, want auditcore", i, got)
		}
		if got := ops[i].labels["definition_id"]; got != "def-x" {
			t.Errorf("ops[%d].definition_id = %q, want def-x", i, got)
		}
		// step_name must NOT be a label (cardinality control).
		if _, ok := ops[i].labels["step_name"]; ok {
			t.Errorf("ops[%d] contains step_name label — outcome counter must exclude step_name", i)
		}
	}
}

// TestSagaStepCollector_LabelSet_OutcomeCounter freezes the outcome counter
// label set (cell, definition_id, outcome) — guards against accidentally
// adding step_name (Cartesian explosion).
func TestSagaStepCollector_LabelSet_OutcomeCounter(t *testing.T) {
	p := newSagaSpyProvider()
	_, err := obmetrics.NewSagaStepCollector(p, "configcore")
	if err != nil {
		t.Fatalf("NewSagaStepCollector: %v", err)
	}
	got := p.counterLabels["saga_step_outcome_total"]
	want := []string{"cell", "definition_id", "outcome"}
	if !equalStringSlice(got, want) {
		t.Errorf("saga_step_outcome_total labels = %v, want %v", got, want)
	}
}

// TestSagaStepCollector_LabelSet_RetryCounter freezes the retry counter label
// set (cell, definition_id, step_name) — guards against adding outcome
// (would create attempt×outcome miscounting since retry fires before outcome).
func TestSagaStepCollector_LabelSet_RetryCounter(t *testing.T) {
	p := newSagaSpyProvider()
	_, err := obmetrics.NewSagaStepCollector(p, "configcore")
	if err != nil {
		t.Fatalf("NewSagaStepCollector: %v", err)
	}
	got := p.counterLabels["saga_step_retry_total"]
	want := []string{"cell", "definition_id", "step_name"}
	if !equalStringSlice(got, want) {
		t.Errorf("saga_step_retry_total labels = %v, want %v", got, want)
	}
}

// TestSagaStepCollector_LabelSet_HeartbeatCounter freezes the hb counter label
// set (cell, reason) — heartbeat is an infra signal, not per-step.
func TestSagaStepCollector_LabelSet_HeartbeatCounter(t *testing.T) {
	p := newSagaSpyProvider()
	_, err := obmetrics.NewSagaStepCollector(p, "configcore")
	if err != nil {
		t.Fatalf("NewSagaStepCollector: %v", err)
	}
	got := p.counterLabels["saga_heartbeat_failed_total"]
	want := []string{"cell", "reason"}
	if !equalStringSlice(got, want) {
		t.Errorf("saga_heartbeat_failed_total labels = %v, want %v", got, want)
	}
}

// TestSagaStepCollector_ObserveRetry_IncrementsWithStepName verifies retry
// increments include step_name (per-step retry budget tuning needs it).
func TestSagaStepCollector_ObserveRetry_IncrementsWithStepName(t *testing.T) {
	p := newSagaSpyProvider()
	c, err := obmetrics.NewSagaStepCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaStepCollector: %v", err)
	}
	c.ObserveRetry(context.Background(), "def-y", "step-validate")

	ops := p.counterOps["saga_step_retry_total"]
	if len(ops) != 1 {
		t.Fatalf("retry counter ops = %d, want 1", len(ops))
	}
	if ops[0].labels["step_name"] != "step-validate" {
		t.Errorf("step_name = %q, want step-validate", ops[0].labels["step_name"])
	}
	if ops[0].labels["definition_id"] != "def-y" {
		t.Errorf("definition_id = %q, want def-y", ops[0].labels["definition_id"])
	}
}

// TestSagaStepCollector_ObserveHeartbeatFailure_BothReasons verifies both
// reason values (infra_error, stale_lease) are passed through as label
// values verbatim.
func TestSagaStepCollector_ObserveHeartbeatFailure_BothReasons(t *testing.T) {
	p := newSagaSpyProvider()
	c, err := obmetrics.NewSagaStepCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaStepCollector: %v", err)
	}
	c.ObserveHeartbeatFailure(context.Background(), executor.HeartbeatFailureInfraError)
	c.ObserveHeartbeatFailure(context.Background(), executor.HeartbeatFailureStaleLease)

	ops := p.counterOps["saga_heartbeat_failed_total"]
	if len(ops) != 2 {
		t.Fatalf("hb counter ops = %d, want 2", len(ops))
	}
	if ops[0].labels["reason"] != "infra_error" {
		t.Errorf("ops[0].reason = %q, want infra_error", ops[0].labels["reason"])
	}
	if ops[1].labels["reason"] != "stale_lease" {
		t.Errorf("ops[1].reason = %q, want stale_lease", ops[1].labels["reason"])
	}
}

// TestSagaStepCollector_NopObserver_DoesNotPanic verifies that the NopObserver
// path (used when WithObserver(nil) is passed to the Executor) does not panic.
// A nil *SagaStepCollector must never reach ObserveOutcome/ObserveRetry/
// ObserveHeartbeatFailure — callers must use executor.NopObserver instead
// (see NewSagaStepCollector caller contract).
func TestSagaStepCollector_NopObserver_DoesNotPanic(t *testing.T) {
	// NopObserver is the zero-cost "no metrics" path; it must never panic.
	p := newSagaSpyProvider()
	c, err := obmetrics.NewSagaStepCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaStepCollector: %v", err)
	}
	// Verify that a properly constructed (non-nil) collector does not panic on all methods.
	c.ObserveOutcome(context.Background(), "d", "s", executor.OutcomeSucceeded, 1)
	c.ObserveRetry(context.Background(), "d", "s")
	c.ObserveHeartbeatFailure(context.Background(), executor.HeartbeatFailureInfraError)
}

// TestOutcomeLabel_UnknownVariant_Panics asserts that outcomeLabel panics when
// passed an unknown Outcome value. This is an A-class programmer error: the
// Outcome enum is exhaustive and any unrecognized value indicates a broken
// caller.
func TestOutcomeLabel_UnknownVariant_Panics(t *testing.T) {
	p := newSagaSpyProvider()
	c, err := obmetrics.NewSagaStepCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaStepCollector: %v", err)
	}
	// Outcome(99) is not declared in executor and must trigger a panic via
	// the panicregister.Approved funnel (A-class unreachable state machine branch).
	defer func() {
		if recover() == nil {
			t.Fatal("ObserveOutcome with unknown Outcome must panic; got nil recover")
		}
	}()
	c.ObserveOutcome(context.Background(), "def", "step", executor.Outcome(99), 1)
}

// TestNewSagaStepCollector_NoHistograms freezes the invariant that
// SagaStepCollector registers only CounterVec metrics (no histograms).
// Accidentally registering a histogram would change the cardinality discipline
// and surprise dashboard / alert owners; this test guards against drift.
func TestNewSagaStepCollector_NoHistograms(t *testing.T) {
	p := newSagaSpyProvider()
	_, err := obmetrics.NewSagaStepCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaStepCollector: %v", err)
	}
	if len(p.histogramNames) != 0 {
		t.Errorf("SagaStepCollector must not register histograms, got %v", p.histogramNames)
	}
}

// ---------------------------------------------------------------------------
// sagaSpyProvider — tracks counter registrations and emissions, including
// declared label sets (for the LabelSet_* freeze tests).
// ---------------------------------------------------------------------------

type sagaSpyRecord struct {
	labels kernelmetrics.Labels
	value  float64
}

type sagaSpyProvider struct {
	counterNames   map[string]struct{}
	counterLabels  map[string][]string
	gaugeNames     map[string]struct{}
	histogramNames map[string]struct{}
	counterOps     map[string][]sagaSpyRecord
}

func newSagaSpyProvider() *sagaSpyProvider {
	return &sagaSpyProvider{
		counterNames:   make(map[string]struct{}),
		counterLabels:  make(map[string][]string),
		gaugeNames:     make(map[string]struct{}),
		histogramNames: make(map[string]struct{}),
		counterOps:     make(map[string][]sagaSpyRecord),
	}
}

func (p *sagaSpyProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	p.counterNames[opts.Name] = struct{}{}
	p.counterLabels[opts.Name] = append([]string(nil), opts.LabelNames...)
	return &sagaSpyCounterVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *sagaSpyProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	p.histogramNames[opts.Name] = struct{}{}
	return kernelmetrics.NopProvider{}.HistogramVec(opts)
}

func (p *sagaSpyProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	p.gaugeNames[opts.Name] = struct{}{}
	return kernelmetrics.NopProvider{}.GaugeVec(opts)
}

func (p *sagaSpyProvider) Unregister(_ kernelmetrics.Collector) error { return nil }

type sagaSpyCounterVec struct {
	parent     *sagaSpyProvider
	name       string
	labelNames []string
}

func (v *sagaSpyCounterVec) Registered() bool { return true }
func (v *sagaSpyCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	kernelmetrics.MustValidateLabels(v.labelNames, l)
	return &sagaSpyCounter{parent: v.parent, name: v.name, labels: l}
}

type sagaSpyCounter struct {
	parent *sagaSpyProvider
	name   string
	labels kernelmetrics.Labels
}

func (c *sagaSpyCounter) Inc(ctx context.Context) { c.Add(ctx, 1) }
func (c *sagaSpyCounter) Add(_ context.Context, d float64) {
	c.parent.counterOps[c.name] = append(c.parent.counterOps[c.name], sagaSpyRecord{labels: c.labels, value: d})
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
