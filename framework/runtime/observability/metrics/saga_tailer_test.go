package metrics_test

import (
	"context"
	"testing"
	"time"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
	"github.com/ghbvf/gocell/framework/runtime/saga/tailer"
)

var (
	_ kernelmetrics.Provider = (*tlSpyProvider)(nil)
	_ tailer.Observer        = (*obmetrics.SagaTailerCollector)(nil)
)

const tlCell, tlProj = "auditcore", "sagastatus"

func TestNewSagaTailerCollector_RejectsNilProvider(t *testing.T) {
	if _, err := obmetrics.NewSagaTailerCollector(nil, tlCell); err == nil {
		t.Fatal("want error for nil provider")
	}
}

func TestNewSagaTailerCollector_RejectsEmptyCellID(t *testing.T) {
	if _, err := obmetrics.NewSagaTailerCollector(kernelmetrics.NopProvider{}, ""); err == nil {
		t.Fatal("want error for empty cellID")
	}
}

func TestSagaTailerCollector_RegistersThreeCountersTwoGauges(t *testing.T) {
	p := newTLSpy()
	if _, err := obmetrics.NewSagaTailerCollector(p, tlCell); err != nil {
		t.Fatalf("NewSagaTailerCollector: %v", err)
	}
	wantCounters := []string{
		"saga_journal_tailer_lock_acquire_failed_total",
		"saga_journal_tailer_drain_total",
		"saga_journal_tailer_checkpoint_advance_total",
	}
	wantGauges := []string{
		"saga_journal_tailer_pending_events",
		"saga_journal_tailer_last_success_timestamp_seconds",
	}
	for _, n := range wantCounters {
		if _, ok := p.counterNames[n]; !ok {
			t.Errorf("missing counter %q", n)
		}
	}
	for _, n := range wantGauges {
		if _, ok := p.gaugeNames[n]; !ok {
			t.Errorf("missing gauge %q", n)
		}
	}
	if len(p.counterNames) != len(wantCounters) {
		t.Errorf("counter count = %d, want %d", len(p.counterNames), len(wantCounters))
	}
	if len(p.gaugeNames) != len(wantGauges) {
		t.Errorf("gauge count = %d, want %d", len(p.gaugeNames), len(wantGauges))
	}
}

func TestSagaTailerCollector_ObserveLockAcquire_AllReasons(t *testing.T) {
	p := newTLSpy()
	c, _ := obmetrics.NewSagaTailerCollector(p, tlCell)
	for _, r := range []tailer.LockAcquireResult{tailer.LockContended, tailer.LockCtxCanceled, tailer.LockBackendError} {
		c.ObserveLockAcquire(context.Background(), tlProj, r)
	}
	ops := p.counterOps["saga_journal_tailer_lock_acquire_failed_total"]
	wants := []string{"contended", "ctx_canceled", "backend_error"}
	if len(ops) != len(wants) {
		t.Fatalf("ops = %d, want %d", len(ops), len(wants))
	}
	for i, w := range wants {
		if ops[i].labels["reason"] != w || ops[i].labels["cell"] != tlCell || ops[i].labels["projection"] != tlProj {
			t.Errorf("ops[%d] = %v, want reason=%s cell=%s projection=%s", i, ops[i].labels, w, tlCell, tlProj)
		}
	}
}

func TestSagaTailerCollector_ObserveDrainAndAdvance(t *testing.T) {
	p := newTLSpy()
	c, _ := obmetrics.NewSagaTailerCollector(p, tlCell)
	// Exercise the full DrainResult / AdvanceResult enums so that adding a value
	// (e.g. DrainHeadError) without covering it here is caught. Expected label
	// values derive from the same ordered enum slices, so there is no parallel
	// string list to drift out of sync with the enum.
	drainResults := []tailer.DrainResult{
		tailer.DrainOK, tailer.DrainHeadError, tailer.DrainStoreError, tailer.DrainApplyError,
	}
	for _, r := range drainResults {
		c.ObserveDrain(context.Background(), tlProj, r)
	}
	advanceResults := []tailer.AdvanceResult{
		tailer.AdvanceOK, tailer.AdvanceStaleOwner, tailer.AdvanceError,
	}
	for _, r := range advanceResults {
		c.ObserveCheckpointAdvance(context.Background(), tlProj, r)
	}

	drains := labelValues(p.counterOps["saga_journal_tailer_drain_total"], "result")
	if want := enumStrings(drainResults); !equalStringSlice(drains, want) {
		t.Errorf("drain results = %v, want %v", drains, want)
	}
	adv := labelValues(p.counterOps["saga_journal_tailer_checkpoint_advance_total"], "result")
	if want := enumStrings(advanceResults); !equalStringSlice(adv, want) {
		t.Errorf("advance results = %v, want %v", adv, want)
	}
}

func TestSagaTailerCollector_Gauges(t *testing.T) {
	p := newTLSpy()
	c, _ := obmetrics.NewSagaTailerCollector(p, tlCell)
	c.ObserveLag(context.Background(), tlProj, 42)
	c.ObserveLastSuccess(context.Background(), tlProj, time.Unix(1700000000, 0))

	pend := p.gaugeOps["saga_journal_tailer_pending_events"]
	if len(pend) != 1 || pend[0].value != 42 || pend[0].labels["projection"] != tlProj {
		t.Errorf("pending gauge ops = %v, want one Set(42) for projection", pend)
	}
	last := p.gaugeOps["saga_journal_tailer_last_success_timestamp_seconds"]
	if len(last) != 1 || last[0].value != 1700000000 {
		t.Errorf("last_success gauge ops = %v, want one Set(1700000000)", last)
	}
}

func TestSagaTailerCollector_LabelSets(t *testing.T) {
	p := newTLSpy()
	if _, err := obmetrics.NewSagaTailerCollector(p, tlCell); err != nil {
		t.Fatalf("NewSagaTailerCollector: %v", err)
	}
	cl := map[string][]string{
		"saga_journal_tailer_lock_acquire_failed_total": {"cell", "projection", "reason"},
		"saga_journal_tailer_drain_total":               {"cell", "projection", "result"},
		"saga_journal_tailer_checkpoint_advance_total":  {"cell", "projection", "result"},
	}
	for name, want := range cl {
		if !equalStringSlice(p.counterLabels[name], want) {
			t.Errorf("%s labels = %v, want %v", name, p.counterLabels[name], want)
		}
	}
	gl := map[string][]string{
		"saga_journal_tailer_pending_events":                 {"cell", "projection"},
		"saga_journal_tailer_last_success_timestamp_seconds": {"cell", "projection"},
	}
	for name, want := range gl {
		if !equalStringSlice(p.gaugeLabels[name], want) {
			t.Errorf("%s labels = %v, want %v", name, p.gaugeLabels[name], want)
		}
	}
}

func TestSagaTailerCollector_RegistrationRollback(t *testing.T) {
	p := newTLSpy()
	p.failOnName = "saga_journal_tailer_drain_total" // second registration fails
	if _, err := obmetrics.NewSagaTailerCollector(p, tlCell); err == nil {
		t.Fatal("want error when a registration fails")
	}
	// The first counter must have been rolled back (Unregister called).
	if p.unregisterCount == 0 {
		t.Error("want LIFO rollback Unregister on partial registration")
	}
}

// labelValues extracts the value of label key from each op, in order.
func labelValues(ops []tlSpyRecord, key string) []string {
	out := make([]string, len(ops))
	for i, op := range ops {
		out[i] = op.labels[key]
	}
	return out
}

// enumStrings maps an ordered slice of string-kind enum values to their wire
// string forms, so a test's expected label set derives from the same enum
// constants it exercises (no parallel string list to drift).
func enumStrings[T ~string](vs []T) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = string(v)
	}
	return out
}

// ── tlSpyProvider: records counter + gauge registrations and ops ────────────

type tlSpyRecord struct {
	labels kernelmetrics.Labels
	value  float64
}

type tlSpyProvider struct {
	counterNames    map[string]struct{}
	counterLabels   map[string][]string
	counterOps      map[string][]tlSpyRecord
	gaugeNames      map[string]struct{}
	gaugeLabels     map[string][]string
	gaugeOps        map[string][]tlSpyRecord
	failOnName      string
	unregisterCount int
}

func newTLSpy() *tlSpyProvider {
	return &tlSpyProvider{
		counterNames:  map[string]struct{}{},
		counterLabels: map[string][]string{},
		counterOps:    map[string][]tlSpyRecord{},
		gaugeNames:    map[string]struct{}{},
		gaugeLabels:   map[string][]string{},
		gaugeOps:      map[string][]tlSpyRecord{},
	}
}

func (p *tlSpyProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	if opts.Name == p.failOnName {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal, "tlSpy: injected failure")
	}
	p.counterNames[opts.Name] = struct{}{}
	p.counterLabels[opts.Name] = append([]string(nil), opts.LabelNames...)
	return &tlSpyCounterVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *tlSpyProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	if opts.Name == p.failOnName {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal, "tlSpy: injected failure")
	}
	p.gaugeNames[opts.Name] = struct{}{}
	p.gaugeLabels[opts.Name] = append([]string(nil), opts.LabelNames...)
	return &tlSpyGaugeVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *tlSpyProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	return kernelmetrics.NopProvider{}.HistogramVec(opts)
}

func (p *tlSpyProvider) Unregister(kernelmetrics.Collector) error {
	p.unregisterCount++
	return nil
}

type tlSpyCounterVec struct {
	parent     *tlSpyProvider
	name       string
	labelNames []string
}

func (v *tlSpyCounterVec) Registered() bool { return true }
func (v *tlSpyCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	kernelmetrics.MustValidateLabels(v.labelNames, l)
	return &tlSpyCounter{parent: v.parent, name: v.name, labels: l}
}

type tlSpyCounter struct {
	parent *tlSpyProvider
	name   string
	labels kernelmetrics.Labels
}

func (c *tlSpyCounter) Inc(ctx context.Context) { c.Add(ctx, 1) }
func (c *tlSpyCounter) Add(_ context.Context, d float64) {
	c.parent.counterOps[c.name] = append(c.parent.counterOps[c.name], tlSpyRecord{labels: c.labels, value: d})
}

type tlSpyGaugeVec struct {
	parent     *tlSpyProvider
	name       string
	labelNames []string
}

func (v *tlSpyGaugeVec) Registered() bool { return true }
func (v *tlSpyGaugeVec) With(l kernelmetrics.Labels) kernelmetrics.Gauge {
	kernelmetrics.MustValidateLabels(v.labelNames, l)
	return &tlSpyGauge{parent: v.parent, name: v.name, labels: l}
}

type tlSpyGauge struct {
	parent *tlSpyProvider
	name   string
	labels kernelmetrics.Labels
}

func (g *tlSpyGauge) Set(_ context.Context, v float64) {
	g.parent.gaugeOps[g.name] = append(g.parent.gaugeOps[g.name], tlSpyRecord{labels: g.labels, value: v})
}
func (g *tlSpyGauge) Inc(ctx context.Context)            { g.Set(ctx, 0) }
func (g *tlSpyGauge) Dec(ctx context.Context)            { g.Set(ctx, 0) }
func (g *tlSpyGauge) Add(ctx context.Context, _ float64) { g.Set(ctx, 0) }
