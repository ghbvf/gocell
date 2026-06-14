package webhook

import (
	"context"
	"slices"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
)

// TestRegisterWebhookMetrics_WiresFour asserts RegisterMetrics registers all
// four webhook instruments with the canonical names and label sets.
func TestRegisterWebhookMetrics_WiresFour(t *testing.T) {
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	if m.deliveries == nil || m.deliveryDuration == nil ||
		m.signatureFailures == nil || m.idempotencyHits == nil {
		t.Fatalf("RegisterMetrics left a nil instrument: %+v", m)
	}
	wantLabels := map[string][]string{
		"webhook_deliveries_total":          {"result", "source"},
		"webhook_delivery_duration_seconds": {"source"},
		"webhook_signature_failures_total":  {"source", "reason"},
		"webhook_idempotency_hits_total":    {"source"},
	}
	for name, want := range wantLabels {
		got := p.labelsOf(name)
		if !equalStrings(got, want) {
			t.Errorf("metric %q labels = %v, want %v", name, got, want)
		}
	}
}

// TestWebhookMetrics_RecordDelivery asserts each frozen delivery-result value
// increments webhook_deliveries_total{result,source}.
func TestWebhookMetrics_RecordDelivery(t *testing.T) {
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	results := []webhookDeliveryResult{
		deliverySuccess, deliveryClientError, deliveryServerError,
		deliveryTransportError, deliveryBlocked, deliveryCircuitOpen,
	}
	ctx := context.Background()
	for _, r := range results {
		m.recordDelivery(ctx, "stripe", r)
		got := p.counterValue("webhook_deliveries_total",
			kernelmetrics.Labels{"result": string(r), "source": "stripe"})
		if got != 1 {
			t.Errorf("recordDelivery(%q): counter = %d, want 1", r, got)
		}
	}
}

// TestWebhookMetrics_ObserveDeliveryDuration asserts the histogram observes one
// sample per call under {source}.
func TestWebhookMetrics_ObserveDeliveryDuration(t *testing.T) {
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	m.observeDeliveryDuration(context.Background(), "stripe", 0.42)
	if c := p.histogramCount("webhook_delivery_duration_seconds",
		kernelmetrics.Labels{"source": "stripe"}); c != 1 {
		t.Errorf("histogram count = %d, want 1", c)
	}
}

// TestWebhookMetrics_RecordSignatureFailure asserts each frozen reason value
// increments webhook_signature_failures_total{source,reason}.
func TestWebhookMetrics_RecordSignatureFailure(t *testing.T) {
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	reasons := []SignatureFailureReason{
		ReasonMissingHeader, ReasonInvalidHeader, ReasonUnknownSource,
		ReasonBadSignature, ReasonTimestampExpired,
	}
	ctx := context.Background()
	for _, r := range reasons {
		m.RecordSignatureFailure(ctx, "stripe", r)
		got := p.counterValue("webhook_signature_failures_total",
			kernelmetrics.Labels{"source": "stripe", "reason": string(r)})
		if got != 1 {
			t.Errorf("RecordSignatureFailure(%q): counter = %d, want 1", r, got)
		}
	}
}

// TestWebhookMetrics_RecordIdempotencyHit asserts the duplicate-delivery counter
// increments under {source}.
func TestWebhookMetrics_RecordIdempotencyHit(t *testing.T) {
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	m.RecordIdempotencyHit(context.Background(), "stripe")
	if got := p.counterValue("webhook_idempotency_hits_total",
		kernelmetrics.Labels{"source": "stripe"}); got != 1 {
		t.Errorf("idempotency hit counter = %d, want 1", got)
	}
}

// TestWebhookMetrics_NilInstrumentsNoPanic asserts the zero Metrics value (all
// instruments nil — the disabled/NopProvider case) records as a no-op without
// panicking.
func TestWebhookMetrics_NilInstrumentsNoPanic(t *testing.T) {
	var m Metrics
	ctx := context.Background()
	m.recordDelivery(ctx, "s", deliverySuccess)
	m.observeDeliveryDuration(ctx, "s", 1.0)
	m.RecordSignatureFailure(ctx, "s", ReasonBadSignature)
	m.RecordIdempotencyHit(ctx, "s")
}

// TestWebhookMetrics_PreflightClean asserts RegisterMetrics' canonical label
// sets validate without panic.
func TestWebhookMetrics_PreflightClean(t *testing.T) {
	m, err := RegisterMetrics(newRecordingProvider())
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	if err := m.preflight(); err != nil {
		t.Fatalf("preflight: %v", err)
	}
}

// TestWebhookMetrics_DeliveryDurationBucketsFrozen asserts the EXACT bucket
// boundaries RegisterMetrics registers for webhook_delivery_duration_seconds
// (F5). These buckets are an operational contract — dashboards, SLO burn-rate
// rules, and histogram_quantile() recording rules pin to these le boundaries, so
// an accidental edit (a dropped 30s tail, a reordered split) would silently break
// latency panels. The assertion is against the registered slice (what the
// provider received), not just the package var, so it also catches a future
// refactor that passes a different slice to HistogramVec. Compared against an
// independent hardcoded golden (anti-tautology): update both in the same PR if
// the bucket contract intentionally changes, alongside the dashboards/alerts.
func TestWebhookMetrics_DeliveryDurationBucketsFrozen(t *testing.T) {
	p := newRecordingProvider()
	if _, err := RegisterMetrics(p); err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	want := []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30}
	got := p.bucketsOf("webhook_delivery_duration_seconds")
	if !slices.Equal(got, want) {
		t.Errorf("webhook_delivery_duration_seconds buckets = %v, want %v "+
			"(ops contract: update dashboards/alerts + this golden together)", got, want)
	}
}

// --- recordingProvider: in-memory metrics.Provider spy for webhook tests. ---

// recordingProvider embeds NopProvider so the unused GaugeVec / Unregister
// methods are inherited (webhook metrics have no gauges); it overrides only the
// CounterVec / HistogramVec it needs to record.
type recordingProvider struct {
	kernelmetrics.NopProvider
	counters   map[string]*recordingCounterVec
	histograms map[string]*recordingHistogramVec
}

func newRecordingProvider() *recordingProvider {
	return &recordingProvider{
		counters:   map[string]*recordingCounterVec{},
		histograms: map[string]*recordingHistogramVec{},
	}
}

func (p *recordingProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	v := &recordingCounterVec{labels: append([]string(nil), opts.LabelNames...), obs: map[string]int64{}}
	p.counters[opts.Name] = v
	return v, nil
}

func (p *recordingProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	v := &recordingHistogramVec{
		labels:  append([]string(nil), opts.LabelNames...),
		buckets: append([]float64(nil), opts.Buckets...),
		obs:     map[string]int64{},
	}
	p.histograms[opts.Name] = v
	return v, nil
}

// bucketsOf returns the exact bucket boundaries RegisterMetrics passed to the
// provider for the named histogram (the registered ops contract), or nil.
func (p *recordingProvider) bucketsOf(name string) []float64 {
	if h, ok := p.histograms[name]; ok {
		return h.buckets
	}
	return nil
}

func (p *recordingProvider) labelsOf(name string) []string {
	if c, ok := p.counters[name]; ok {
		return c.labels
	}
	if h, ok := p.histograms[name]; ok {
		return h.labels
	}
	return nil
}

func (p *recordingProvider) counterValue(name string, l kernelmetrics.Labels) int64 {
	c, ok := p.counters[name]
	if !ok {
		return -1
	}
	return c.obs[labelKey(c.labels, l)]
}

func (p *recordingProvider) histogramCount(name string, l kernelmetrics.Labels) int64 {
	h, ok := p.histograms[name]
	if !ok {
		return -1
	}
	return h.obs[labelKey(h.labels, l)]
}

type recordingCounterVec struct {
	labels []string
	obs    map[string]int64
}

func (v *recordingCounterVec) Registered() bool { return true }
func (v *recordingCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return &recordingCounter{vec: v, key: labelKey(v.labels, l)}
}

type recordingCounter struct {
	vec *recordingCounterVec
	key string
}

func (c *recordingCounter) Inc(_ context.Context)            { c.vec.obs[c.key]++ }
func (c *recordingCounter) Add(_ context.Context, n float64) { c.vec.obs[c.key] += int64(n) }

type recordingHistogramVec struct {
	labels  []string
	buckets []float64
	obs     map[string]int64
}

func (v *recordingHistogramVec) Registered() bool { return true }
func (v *recordingHistogramVec) With(l kernelmetrics.Labels) kernelmetrics.Histogram {
	kernelmetrics.MustValidateLabels(v.labels, l)
	return &recordingHistogram{vec: v, key: labelKey(v.labels, l)}
}

type recordingHistogram struct {
	vec *recordingHistogramVec
	key string
}

func (h *recordingHistogram) Observe(_ context.Context, _ float64) { h.vec.obs[h.key]++ }

func labelKey(order []string, l kernelmetrics.Labels) string {
	key := ""
	for _, k := range order {
		key += k + "=" + l[k] + ";"
	}
	return key
}

func equalStrings(a, b []string) bool {
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

var _ kernelmetrics.Provider = (*recordingProvider)(nil)
