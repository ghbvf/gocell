package audit_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/runtime/audit"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

// --- fake ledger.ChainVerifyStore ------------------------------------------

type fakeVerifyStore struct {
	chains  []ledger.ChainRef
	enumErr error
	verify  func(ns, tenant string, from, to int64) (bool, int64, error)
}

func (f *fakeVerifyStore) EnumerateChains(context.Context) ([]ledger.ChainRef, error) {
	if f.enumErr != nil {
		return nil, f.enumErr
	}
	return f.chains, nil
}

func (f *fakeVerifyStore) VerifyChain(_ context.Context, ns, tenant string, from, to int64) (bool, int64, error) {
	if f.verify != nil {
		return f.verify(ns, tenant, from, to)
	}
	return true, -1, nil
}

// --- recording metrics.Provider --------------------------------------------

type recordingProvider struct {
	mu         sync.Mutex
	registered map[string][]string // metric name -> sorted label names
	withCalls  []metrics.Labels    // every With() label map across all vecs
}

func newRecordingProvider() *recordingProvider {
	return &recordingProvider{registered: map[string][]string{}}
}

func (p *recordingProvider) record(name string, labels []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := append([]string{}, labels...) // always non-nil so golden compares empty==empty
	sort.Strings(cp)
	p.registered[name] = cp
}

func (p *recordingProvider) recordWith(declared []string, l metrics.Labels) {
	metrics.MustValidateLabels(declared, l) // mirror production label-drift enforcement
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := metrics.Labels{}
	for k, v := range l {
		cp[k] = v
	}
	p.withCalls = append(p.withCalls, cp)
}

func (p *recordingProvider) CounterVec(o metrics.CounterOpts) (metrics.CounterVec, error) {
	p.record(o.Name, o.LabelNames)
	return &recCounterVec{p: p, labels: o.LabelNames}, nil
}

func (p *recordingProvider) HistogramVec(o metrics.HistogramOpts) (metrics.HistogramVec, error) {
	p.record(o.Name, o.LabelNames)
	return &recHistVec{p: p, labels: o.LabelNames}, nil
}

func (p *recordingProvider) GaugeVec(o metrics.GaugeOpts) (metrics.GaugeVec, error) {
	p.record(o.Name, o.LabelNames)
	return &recGaugeVec{p: p, labels: o.LabelNames}, nil
}

func (p *recordingProvider) Unregister(metrics.Collector) error { return nil }

type recPoint struct{}

func (recPoint) Inc(context.Context)              {}
func (recPoint) Dec(context.Context)              {}
func (recPoint) Add(context.Context, float64)     {}
func (recPoint) Set(context.Context, float64)     {}
func (recPoint) Observe(context.Context, float64) {}

type recCounterVec struct {
	p      *recordingProvider
	labels []string
}

func (v *recCounterVec) Registered() bool { return true }
func (v *recCounterVec) With(l metrics.Labels) metrics.Counter {
	v.p.recordWith(v.labels, l)
	return recPoint{}
}

type recHistVec struct {
	p      *recordingProvider
	labels []string
}

func (v *recHistVec) Registered() bool { return true }
func (v *recHistVec) With(l metrics.Labels) metrics.Histogram {
	v.p.recordWith(v.labels, l)
	return recPoint{}
}

type recGaugeVec struct {
	p      *recordingProvider
	labels []string
}

func (v *recGaugeVec) Registered() bool { return true }
func (v *recGaugeVec) With(l metrics.Labels) metrics.Gauge {
	v.p.recordWith(v.labels, l)
	return recPoint{}
}

// --- helpers ----------------------------------------------------------------

func newTestVerifier(t *testing.T, store ledger.ChainVerifyStore, mp metrics.Provider) *audit.ChainVerifier {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	v, err := audit.NewChainVerifier(store, mp, clockmock.New(time.Now()), logger)
	if err != nil {
		t.Fatalf("NewChainVerifier: %v", err)
	}
	return v
}

func ref(ns, tenant string, minSeq, maxSeq int64) ledger.ChainRef {
	return ledger.ChainRef{Namespace: ns, TenantID: tenant, MinSeq: minSeq, MaxSeq: maxSeq}
}

// --- tests ------------------------------------------------------------------

func TestNewChainVerifier_NilStore(t *testing.T) {
	t.Parallel()
	if _, err := audit.NewChainVerifier(nil, metrics.NopProvider{}, clockmock.New(time.Now()), nil); err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestVerifyAll_AllValid(t *testing.T) {
	t.Parallel()
	store := &fakeVerifyStore{chains: []ledger.ChainRef{
		ref("auditcore", "t-a", 1, 3),
		ref("bootstrap", "", 1, 2),
	}}
	mp := newRecordingProvider()
	report, err := newTestVerifier(t, store, mp).VerifyAll(context.Background())
	if err != nil {
		t.Fatalf("VerifyAll: %v", err)
	}
	if !report.AllValid() || report.TotalChains != 2 || report.InvalidChains != 0 || report.ErroredChains != 0 {
		t.Fatalf("report=%+v, want all-valid 2 chains", report)
	}
	if got := mp.registered["audit_chain_verify_runs_total"]; len(got) != 1 || got[0] != "outcome" {
		t.Fatalf("runs_total labels=%v, want [outcome]", got)
	}
}

func TestVerifyAll_OneInvalid(t *testing.T) {
	t.Parallel()
	store := &fakeVerifyStore{
		chains: []ledger.ChainRef{ref("auditcore", "t-a", 1, 3), ref("auditcore", "t-b", 1, 2)},
		verify: func(_, tenant string, _, _ int64) (bool, int64, error) {
			if tenant == "t-b" {
				return false, 2, nil // tamper at seq 2
			}
			return true, -1, nil
		},
	}
	report, err := newTestVerifier(t, store, newRecordingProvider()).VerifyAll(context.Background())
	if err != nil {
		t.Fatalf("VerifyAll: %v", err)
	}
	if report.AllValid() || report.InvalidChains != 1 || report.ErroredChains != 0 {
		t.Fatalf("report=%+v, want 1 invalid", report)
	}
	// The invalid result must carry its first-invalid seq.
	var found bool
	for _, r := range report.Results {
		if r.TenantID == "t-b" {
			found = true
			if r.Valid || r.FirstInvalidSeq != 2 {
				t.Errorf("t-b result=%+v, want invalid@2", r)
			}
		}
	}
	if !found {
		t.Fatal("t-b result missing")
	}
}

func TestVerifyAll_EnumerateError(t *testing.T) {
	t.Parallel()
	store := &fakeVerifyStore{enumErr: errors.New("boom")}
	report, err := newTestVerifier(t, store, newRecordingProvider()).VerifyAll(context.Background())
	if err == nil {
		t.Fatal("expected error from enumerate failure")
	}
	if report.TotalChains != 0 {
		t.Errorf("report.TotalChains=%d, want 0 on enumerate failure", report.TotalChains)
	}
}

func TestVerifyAll_VerifyChainError_RunContinues(t *testing.T) {
	t.Parallel()
	store := &fakeVerifyStore{
		chains: []ledger.ChainRef{ref("auditcore", "t-a", 1, 1), ref("auditcore", "t-b", 1, 1)},
		verify: func(_, tenant string, _, _ int64) (bool, int64, error) {
			if tenant == "t-a" {
				return false, 1, errors.New("infra down")
			}
			return true, -1, nil
		},
	}
	report, err := newTestVerifier(t, store, newRecordingProvider()).VerifyAll(context.Background())
	if err != nil {
		t.Fatalf("VerifyAll must not return an error for a per-chain infra failure: %v", err)
	}
	if report.ErroredChains != 1 || report.TotalChains != 2 {
		t.Fatalf("report=%+v, want 1 errored of 2 (run continues)", report)
	}
}

func TestVerifyAll_EmptyFleet(t *testing.T) {
	t.Parallel()
	report, err := newTestVerifier(t, &fakeVerifyStore{}, newRecordingProvider()).VerifyAll(context.Background())
	if err != nil {
		t.Fatalf("VerifyAll: %v", err)
	}
	if !report.AllValid() || report.TotalChains != 0 {
		t.Fatalf("report=%+v, want all-valid empty fleet", report)
	}
}

func TestVerifyAll_MissingGenesis(t *testing.T) {
	t.Parallel()
	// MinSeq=3 → genesis rows absent. The store reports invalid@1 (the range scan
	// finds the first row at seq 3, gap at expected seq 1). The orchestrator flags
	// MissingGenesis distinctly so it is not mistaken for a seq-1 tamper.
	store := &fakeVerifyStore{
		chains: []ledger.ChainRef{ref("auditcore", "t-a", 3, 5)},
		verify: func(_, _ string, _, _ int64) (bool, int64, error) { return false, 1, nil },
	}
	report, err := newTestVerifier(t, store, newRecordingProvider()).VerifyAll(context.Background())
	if err != nil {
		t.Fatalf("VerifyAll: %v", err)
	}
	if len(report.Results) != 1 || !report.Results[0].MissingGenesis {
		t.Fatalf("result=%+v, want MissingGenesis=true", report.Results)
	}
}

// TestVerifyAll_MetricLabelHygiene mechanically enforces decision #4: NO With()
// call ever carries a tenant_id (or any non-`outcome`) key. tenant_id is unbounded
// and must never become a metric label.
func TestVerifyAll_MetricLabelHygiene(t *testing.T) {
	t.Parallel()
	mp := newRecordingProvider()
	store := &fakeVerifyStore{
		chains: []ledger.ChainRef{ref("auditcore", "tenant-secret-uuid", 1, 2)},
		verify: func(_, _ string, _, _ int64) (bool, int64, error) { return false, 1, nil },
	}
	if _, err := newTestVerifier(t, store, mp).VerifyAll(context.Background()); err != nil {
		t.Fatalf("VerifyAll: %v", err)
	}
	if len(mp.withCalls) == 0 {
		t.Fatal("no metric With() calls recorded — test would be vacuous")
	}
	for i, l := range mp.withCalls {
		for k := range l {
			if k != "outcome" {
				t.Errorf("withCalls[%d] carries forbidden label key %q (only `outcome` allowed)", i, k)
			}
		}
	}
}

// TestChainVerifyMetrics_FrozenSet value-golden-freezes the metric name + label
// set so adding any label (especially tenant_id) or renaming a metric trips here.
func TestChainVerifyMetrics_FrozenSet(t *testing.T) {
	t.Parallel()
	mp := newRecordingProvider()
	// Constructing the verifier registers the instruments.
	newTestVerifier(t, &fakeVerifyStore{}, mp)
	want := map[string][]string{
		"audit_chain_verify_runs_total":       {"outcome"},
		"audit_chain_verify_invalid_chains":   {},
		"audit_chain_verify_errored_chains":   {},
		"audit_chain_verify_duration_seconds": {},
	}
	if !reflect.DeepEqual(mp.registered, want) {
		t.Fatalf("registered metric/label set drift:\n got=%v\nwant=%v", mp.registered, want)
	}
}

// TestChainVerifyResult_FieldSetFrozen pins the verdict struct's field set so a
// future edit cannot add an audit-row-content field (payload / hash / actor),
// preserving the Hard no-content-leak guarantee (decision #6).
func TestChainVerifyResult_FieldSetFrozen(t *testing.T) {
	t.Parallel()
	var got []string
	rt := reflect.TypeOf(audit.ChainVerifyResult{})
	for i := 0; i < rt.NumField(); i++ {
		got = append(got, rt.Field(i).Name)
	}
	sort.Strings(got)
	want := []string{"Err", "FirstInvalidSeq", "MissingGenesis", "Namespace", "TailSeq", "TenantID", "Valid"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChainVerifyResult field set drift:\n got=%v\nwant=%v", got, want)
	}
}
