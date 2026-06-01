package metrics_test

import (
	"context"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/saga/executor"
)

// Compile-time check: SagaCollector must implement executor.Observer.
var _ executor.Observer = (*obmetrics.SagaCollector)(nil)

// Compile-time check: sagaSpyProvider must implement kernelmetrics.Provider so
// that any future Provider interface expansion is caught at compile time.
var _ kernelmetrics.Provider = (*sagaSpyProvider)(nil)

// TestNewSagaCollector_RejectsNilProvider asserts fail-fast on nil.
func TestNewSagaCollector_RejectsNilProvider(t *testing.T) {
	_, err := obmetrics.NewSagaCollector(nil, "accesscore")
	if err == nil {
		t.Fatal("expected error for nil provider")
	}
}

// TestNewSagaCollector_RejectsEmptyCellID asserts fail-fast on empty cell.
// SagaCollector is per-cell — no _runtime fallback.
func TestNewSagaCollector_RejectsEmptyCellID(t *testing.T) {
	_, err := obmetrics.NewSagaCollector(kernelmetrics.NopProvider{}, "")
	if err == nil {
		t.Fatal("expected error for empty cellID")
	}
}

// TestNewSagaCollector_RegistersSixCounters asserts the six saga counters
// (three step-level + three coordinator-level) are registered (and no others).
func TestNewSagaCollector_RegistersSixCounters(t *testing.T) {
	p := newSagaSpyProvider()
	_, err := obmetrics.NewSagaCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}

	wantCounters := []string{
		"saga_step_outcome_total",
		"saga_step_retry_total",
		"saga_heartbeat_failed_total",
		"saga_tick_total",
		"saga_drive_total",
		"saga_leader_elect_skip_total",
	}
	for _, name := range wantCounters {
		if _, ok := p.counterNames[name]; !ok {
			t.Errorf("SagaCollector did not register %q", name)
		}
	}
	if len(p.counterNames) != len(wantCounters) {
		t.Errorf("counter count = %d, want %d (extras: %v)",
			len(p.counterNames), len(wantCounters), p.counterNames)
	}
	if len(p.gaugeNames) != 0 {
		t.Errorf("SagaCollector must not register gauges, got %v", p.gaugeNames)
	}
}

// TestSagaCollector_ObserveTick_AllResults verifies tick result labels for the
// three TickResult variants.
func TestSagaCollector_ObserveTick_AllResults(t *testing.T) {
	cases := []struct {
		result executor.TickResult
		want   string
	}{
		{executor.TickClaimed, "claimed"},
		{executor.TickEmpty, "empty"},
		{executor.TickError, "error"},
	}
	p := newSagaSpyProvider()
	c, err := obmetrics.NewSagaCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	for _, tc := range cases {
		c.ObserveTick(context.Background(), tc.result)
	}
	ops := p.counterOps["saga_tick_total"]
	if len(ops) != len(cases) {
		t.Fatalf("tick counter ops = %d, want %d", len(ops), len(cases))
	}
	for i, tc := range cases {
		if got := ops[i].labels["result"]; got != tc.want {
			t.Errorf("ops[%d].result = %q, want %q", i, got, tc.want)
		}
		if got := ops[i].labels["cell"]; got != "auditcore" {
			t.Errorf("ops[%d].cell = %q, want auditcore", i, got)
		}
		if _, ok := ops[i].labels["definition_id"]; ok {
			t.Errorf("ops[%d] contains definition_id — tick counter is pre-claim", i)
		}
	}
}

// TestSagaCollector_ObserveDrive_BothResults verifies drive result labels and
// the definition_id dimension.
func TestSagaCollector_ObserveDrive_BothResults(t *testing.T) {
	p := newSagaSpyProvider()
	c, err := obmetrics.NewSagaCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	c.ObserveDrive(context.Background(), "def-d", executor.DriveOK)
	c.ObserveDrive(context.Background(), "def-d", executor.DriveError)

	ops := p.counterOps["saga_drive_total"]
	if len(ops) != 2 {
		t.Fatalf("drive counter ops = %d, want 2", len(ops))
	}
	if ops[0].labels["result"] != "ok" || ops[1].labels["result"] != "error" {
		t.Errorf("drive results = (%q,%q), want (ok,error)", ops[0].labels["result"], ops[1].labels["result"])
	}
	if ops[0].labels["definition_id"] != "def-d" {
		t.Errorf("definition_id = %q, want def-d", ops[0].labels["definition_id"])
	}
}

// TestSagaCollector_ObserveLeaderSkip_AllReasons verifies leader-skip reason
// labels for the three LeaderSkipReason variants (backend_error = lock-acquire
// failure rate per #1109).
func TestSagaCollector_ObserveLeaderSkip_AllReasons(t *testing.T) {
	cases := []struct {
		reason executor.LeaderSkipReason
		want   string
	}{
		{executor.LeaderSkipContended, "contended"},
		{executor.LeaderSkipCtxCanceled, "ctx_canceled"},
		{executor.LeaderSkipBackendError, "backend_error"},
	}
	p := newSagaSpyProvider()
	c, err := obmetrics.NewSagaCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	for _, tc := range cases {
		c.ObserveLeaderSkip(context.Background(), "def-l", tc.reason)
	}
	ops := p.counterOps["saga_leader_elect_skip_total"]
	if len(ops) != len(cases) {
		t.Fatalf("leader-skip counter ops = %d, want %d", len(ops), len(cases))
	}
	for i, tc := range cases {
		if got := ops[i].labels["reason"]; got != tc.want {
			t.Errorf("ops[%d].reason = %q, want %q", i, got, tc.want)
		}
		if ops[i].labels["definition_id"] != "def-l" {
			t.Errorf("ops[%d].definition_id = %q, want def-l", i, ops[i].labels["definition_id"])
		}
	}
}

// TestSagaCollector_LabelSet_TickCounter freezes the tick counter label set.
func TestSagaCollector_LabelSet_TickCounter(t *testing.T) {
	p := newSagaSpyProvider()
	if _, err := obmetrics.NewSagaCollector(p, "configcore"); err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	if got, want := p.counterLabels["saga_tick_total"], []string{"cell", "result"}; !equalStringSlice(got, want) {
		t.Errorf("saga_tick_total labels = %v, want %v", got, want)
	}
}

// TestSagaCollector_LabelSet_DriveCounter freezes the drive counter label set.
func TestSagaCollector_LabelSet_DriveCounter(t *testing.T) {
	p := newSagaSpyProvider()
	if _, err := obmetrics.NewSagaCollector(p, "configcore"); err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	if got, want := p.counterLabels["saga_drive_total"], []string{"cell", "definition_id", "result"}; !equalStringSlice(got, want) {
		t.Errorf("saga_drive_total labels = %v, want %v", got, want)
	}
}

// TestSagaCollector_LabelSet_LeaderSkipCounter freezes the leader-skip counter
// label set.
func TestSagaCollector_LabelSet_LeaderSkipCounter(t *testing.T) {
	p := newSagaSpyProvider()
	if _, err := obmetrics.NewSagaCollector(p, "configcore"); err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	got := p.counterLabels["saga_leader_elect_skip_total"]
	if want := []string{"cell", "definition_id", "reason"}; !equalStringSlice(got, want) {
		t.Errorf("saga_leader_elect_skip_total labels = %v, want %v", got, want)
	}
}

// TestSagaCollector_ObserveOutcome_AllFiveOutcomes verifies wire-stable
// outcome labels for the five Outcome variants.
func TestSagaCollector_ObserveOutcome_AllFiveOutcomes(t *testing.T) {
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
	c, err := obmetrics.NewSagaCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	for _, tc := range cases {
		c.ObserveOutcome(context.Background(), idutil.SafeID("inst-x"), idutil.SafeID("lease-x"), "def-x", "step-1", tc.outcome, 1)
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

// TestSagaCollector_LabelSet_OutcomeCounter freezes the outcome counter
// label set (cell, definition_id, outcome) — guards against accidentally
// adding step_name (Cartesian explosion).
func TestSagaCollector_LabelSet_OutcomeCounter(t *testing.T) {
	p := newSagaSpyProvider()
	_, err := obmetrics.NewSagaCollector(p, "configcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	got := p.counterLabels["saga_step_outcome_total"]
	want := []string{"cell", "definition_id", "outcome"}
	if !equalStringSlice(got, want) {
		t.Errorf("saga_step_outcome_total labels = %v, want %v", got, want)
	}
}

// TestSagaCollector_LabelSet_RetryCounter freezes the retry counter label
// set (cell, definition_id, step_name) — guards against adding outcome
// (would create attempt×outcome miscounting since retry fires before outcome).
func TestSagaCollector_LabelSet_RetryCounter(t *testing.T) {
	p := newSagaSpyProvider()
	_, err := obmetrics.NewSagaCollector(p, "configcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	got := p.counterLabels["saga_step_retry_total"]
	want := []string{"cell", "definition_id", "step_name"}
	if !equalStringSlice(got, want) {
		t.Errorf("saga_step_retry_total labels = %v, want %v", got, want)
	}
}

// TestSagaCollector_LabelSet_HeartbeatCounter freezes the hb counter label
// set (cell, reason) — heartbeat is an infra signal, not per-step.
func TestSagaCollector_LabelSet_HeartbeatCounter(t *testing.T) {
	p := newSagaSpyProvider()
	_, err := obmetrics.NewSagaCollector(p, "configcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	got := p.counterLabels["saga_heartbeat_failed_total"]
	want := []string{"cell", "reason"}
	if !equalStringSlice(got, want) {
		t.Errorf("saga_heartbeat_failed_total labels = %v, want %v", got, want)
	}
}

// TestSagaCollector_ObserveRetry_IncrementsWithStepName verifies retry
// increments include step_name (per-step retry budget tuning needs it).
func TestSagaCollector_ObserveRetry_IncrementsWithStepName(t *testing.T) {
	p := newSagaSpyProvider()
	c, err := obmetrics.NewSagaCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	c.ObserveRetry(context.Background(), idutil.SafeID("inst-y"), idutil.SafeID("lease-y"), "def-y", "step-validate")

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

// TestSagaCollector_ObserveHeartbeatFailure_BothReasons verifies both
// reason values (infra_error, stale_lease) are passed through as label
// values verbatim.
func TestSagaCollector_ObserveHeartbeatFailure_BothReasons(t *testing.T) {
	p := newSagaSpyProvider()
	c, err := obmetrics.NewSagaCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	c.ObserveHeartbeatFailure(context.Background(), idutil.SafeID("inst-hb"), idutil.SafeID("lease-hb"), executor.HeartbeatFailureInfraError)
	c.ObserveHeartbeatFailure(context.Background(), idutil.SafeID("inst-hb"), idutil.SafeID("lease-hb"), executor.HeartbeatFailureStaleLease)

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

// TestSagaCollector_AllMethods_DoNotPanic verifies that every observe method on
// a properly constructed (non-nil) *SagaCollector — the six Observer callbacks
// across the Executor-emitted and Coordinator-emitted groups — executes without
// panicking. The nil-collector / NopObserver contract (callers must pass
// executor.NopObserver via WithObserver(nil), never a nil *SagaCollector) is
// documented on NewSagaCollector; it is not exercised here.
func TestSagaCollector_AllMethods_DoNotPanic(t *testing.T) {
	p := newSagaSpyProvider()
	c, err := obmetrics.NewSagaCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	// Verify that a properly constructed (non-nil) collector does not panic on all methods.
	c.ObserveOutcome(context.Background(), idutil.SafeID("i"), idutil.SafeID("l"), "d", "s", executor.OutcomeSucceeded, 1)
	c.ObserveRetry(context.Background(), idutil.SafeID("i"), idutil.SafeID("l"), "d", "s")
	c.ObserveHeartbeatFailure(context.Background(), idutil.SafeID("i"), idutil.SafeID("l"), executor.HeartbeatFailureInfraError)
	c.ObserveTick(context.Background(), executor.TickClaimed)
	c.ObserveDrive(context.Background(), "d", executor.DriveOK)
	c.ObserveLeaderSkip(context.Background(), "d", executor.LeaderSkipContended)
}

// TestOutcomeLabel_UnknownVariant_Panics asserts that outcomeLabel panics when
// passed an unknown Outcome value. This is an A-class programmer error: the
// Outcome enum is exhaustive and any unrecognized value indicates a broken
// caller.
func TestOutcomeLabel_UnknownVariant_Panics(t *testing.T) {
	p := newSagaSpyProvider()
	c, err := obmetrics.NewSagaCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	// Outcome(99) is not declared in executor and must trigger a panic via
	// the panicregister.Approved funnel (A-class unreachable state machine branch).
	// panicregister.Approved returns value unchanged, so recover() observes the
	// underlying *errcode.Error from errcode.Assertion (the A-class payload).
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("ObserveOutcome with unknown Outcome must panic; got nil recover")
		}
		// Verify the panic payload is *errcode.Error (A-class panicregister.Approved
		// wraps errcode.Assertion — PANIC-REGISTERED-01 funnel verification).
		if _, ok := r.(*errcode.Error); !ok {
			t.Errorf("panic payload must be *errcode.Error (panicregister.Approved A-class); got %T: %v", r, r)
		}
	}()
	c.ObserveOutcome(context.Background(), idutil.SafeID("i"), idutil.SafeID("l"), "def", "step", executor.Outcome(99), 1)
}

// TestNewSagaCollector_NoHistograms freezes the invariant that
// SagaCollector registers only CounterVec metrics (no histograms).
// Accidentally registering a histogram would change the cardinality discipline
// and surprise dashboard / alert owners; this test guards against drift.
func TestNewSagaCollector_NoHistograms(t *testing.T) {
	p := newSagaSpyProvider()
	_, err := obmetrics.NewSagaCollector(p, "auditcore")
	if err != nil {
		t.Fatalf("NewSagaCollector: %v", err)
	}
	if len(p.histogramNames) != 0 {
		t.Errorf("SagaCollector must not register histograms, got %v", p.histogramNames)
	}
}

// TestNewSagaCollector_PartialRegistrationFailure_RollsBack verifies the LIFO
// atomic-registration rollback (#1181 F11): when the 4th counter (saga_tick_total)
// fails to register, the 3 already-registered counters are torn down via
// Unregister so the provider retains no orphans.
func TestNewSagaCollector_PartialRegistrationFailure_RollsBack(t *testing.T) {
	p := newSagaSpyProvider()
	p.failOnName = "saga_tick_total" // the 4th counter in NewSagaCollector order
	_, err := obmetrics.NewSagaCollector(p, "auditcore")
	if err == nil {
		t.Fatal("expected a registration error when saga_tick_total fails")
	}
	// outcome, retry, hbFail were registered before the tick failure → 3 rolled back.
	if p.unregisterCount != 3 {
		t.Errorf("unregisterCount = %d, want 3 (LIFO rollback of the 3 prior counters)", p.unregisterCount)
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

	// failOnName, when non-empty, makes CounterVec return an error for that
	// metric name — exercises the LIFO rollback in NewSagaCollector.
	failOnName      string
	unregisterCount int
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
	if opts.Name == p.failOnName {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"sagaSpyProvider: injected registration failure")
	}
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

func (p *sagaSpyProvider) Unregister(_ kernelmetrics.Collector) error {
	p.unregisterCount++
	return nil
}

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
