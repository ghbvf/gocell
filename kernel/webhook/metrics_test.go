package webhook

import (
	"context"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
)

// TestRegisterWebhookMetrics_WiresFour asserts RegisterMetrics registers all
// four webhook instruments with the canonical names and label sets.
func TestRegisterWebhookMetrics_WiresFour(t *testing.T) {
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	if m.Deliveries == nil || m.DeliveryDuration == nil ||
		m.SignatureFailures == nil || m.IdempotencyHits == nil {
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
		deliveryTransportError, deliveryBlocked,
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

// --- recordingProvider: in-memory metrics.Provider spy for webhook tests. ---

type recordingProvider struct {
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
	v := &recordingHistogramVec{labels: append([]string(nil), opts.LabelNames...), obs: map[string]int64{}}
	p.histograms[opts.Name] = v
	return v, nil
}

func (p *recordingProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	return nil, nil
}

func (p *recordingProvider) Unregister(_ kernelmetrics.Collector) error { return nil }

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
	labels []string
	obs    map[string]int64
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
