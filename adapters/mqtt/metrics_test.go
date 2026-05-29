package mqtt

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestNewProviderConnectionCollector_RejectsNilProvider verifies nil provider is
// rejected with an errcode.Error.
func TestNewProviderConnectionCollector_RejectsNilProvider(t *testing.T) {
	t.Parallel()
	_, err := NewProviderConnectionCollector(nil, "testcell")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
}

// TestNewProviderConnectionCollector_RejectsEmptyCellID verifies empty cellID is
// rejected with an errcode.Error.
func TestNewProviderConnectionCollector_RejectsEmptyCellID(t *testing.T) {
	t.Parallel()
	_, err := NewProviderConnectionCollector(metrics.NopProvider{}, "")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
}

// TestNewProviderConnectionCollector_NopProviderNoPanic exercises the happy path
// on the no-op provider.
func TestNewProviderConnectionCollector_NopProviderNoPanic(t *testing.T) {
	t.Parallel()
	c, err := NewProviderConnectionCollector(metrics.NopProvider{}, "testcell")
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.NotPanics(t, func() { c.RecordReconnect(context.Background()) })
}

// TestNewProviderConnectionCollector_RegistrationFailure verifies a Provider
// returning an error from CounterVec is wrapped in an errcode.Error.
func TestNewProviderConnectionCollector_RegistrationFailure(t *testing.T) {
	t.Parallel()
	provider := &connErrProvider{err: errors.New("duplicate counter")}
	_, err := NewProviderConnectionCollector(provider, "testcell")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
}

// TestProviderConnectionCollector_RecordReconnect_CounterLabel pins that
// RecordReconnect emits exactly one Inc with label cell=testcell.
func TestProviderConnectionCollector_RecordReconnect_CounterLabel(t *testing.T) {
	t.Parallel()
	spy := newConnSpyProvider()
	col, err := NewProviderConnectionCollector(spy, "testcell")
	require.NoError(t, err)

	col.RecordReconnect(context.Background())

	ops := spy.ops()
	require.Len(t, ops, 1)
	assert.Equal(t, "mqtt_reconnect_total", ops[0].name)
	assert.Equal(t, "Inc", ops[0].op)
	assert.Equal(t, "testcell", ops[0].labels["cell"])
}

// TestProviderConnectionCollector_RegistersExpectedLabelNames pins the label
// schema: only "cell".
func TestProviderConnectionCollector_RegistersExpectedLabelNames(t *testing.T) {
	t.Parallel()
	spy := newConnSpyProvider()
	_, err := NewProviderConnectionCollector(spy, "testcell")
	require.NoError(t, err)

	regs := spy.registrations()
	require.Len(t, regs, 1)
	assert.Equal(t, "mqtt_reconnect_total", regs[0].Name)
	assert.Equal(t, []string{"cell"}, regs[0].LabelNames)
}

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// connErrProvider returns a fixed error from CounterVec.
type connErrProvider struct{ err error }

func (p *connErrProvider) CounterVec(_ metrics.CounterOpts) (metrics.CounterVec, error) {
	return nil, p.err
}

func (p *connErrProvider) HistogramVec(_ metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return nil, p.err
}

func (p *connErrProvider) GaugeVec(_ metrics.GaugeOpts) (metrics.GaugeVec, error) {
	return nil, p.err
}
func (p *connErrProvider) Unregister(_ metrics.Collector) error { return nil }

// connSpyProvider records counter registrations and Inc/Add operations.
type connSpyProvider struct {
	regs    []metrics.CounterOpts
	records []connSpyRecord
}

type connSpyRecord struct {
	name   string
	op     string
	labels metrics.Labels
	value  float64
}

func newConnSpyProvider() *connSpyProvider { return &connSpyProvider{} }

func (p *connSpyProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	p.regs = append(p.regs, opts)
	return &connSpyCounterVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *connSpyProvider) HistogramVec(_ metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return nil, errors.New("connSpyProvider: HistogramVec not implemented")
}

func (p *connSpyProvider) GaugeVec(_ metrics.GaugeOpts) (metrics.GaugeVec, error) {
	return metrics.NopProvider{}.GaugeVec(metrics.GaugeOpts{})
}

func (p *connSpyProvider) Unregister(_ metrics.Collector) error { return nil }

func (p *connSpyProvider) ops() []connSpyRecord {
	out := make([]connSpyRecord, len(p.records))
	copy(out, p.records)
	return out
}

func (p *connSpyProvider) registrations() []metrics.CounterOpts {
	out := make([]metrics.CounterOpts, len(p.regs))
	copy(out, p.regs)
	return out
}

type connSpyCounterVec struct {
	parent     *connSpyProvider
	name       string
	labelNames []string
}

func (v *connSpyCounterVec) Registered() bool { return true }

func (v *connSpyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labelNames, l)
	return &connSpyCounter{parent: v.parent, name: v.name, labels: l}
}

type connSpyCounter struct {
	parent *connSpyProvider
	name   string
	labels metrics.Labels
}

func (c *connSpyCounter) Inc(_ context.Context) {
	c.parent.records = append(c.parent.records, connSpyRecord{
		name: c.name, op: "Inc", labels: c.labels, value: 1,
	})
}

func (c *connSpyCounter) Add(_ context.Context, d float64) {
	c.parent.records = append(c.parent.records, connSpyRecord{
		name: c.name, op: "Add", labels: c.labels, value: d,
	})
}

// ---------------------------------------------------------------------------
// Publisher metrics tests
// ---------------------------------------------------------------------------

// Compile-time interface check — kept here so the file fails to build if the
// interface shape diverges.
var _ PublisherCollector = (*providerPublisherCollector)(nil)

// TestNewProviderPublisherCollector_NilProvider verifies nil provider returns
// an errcode.Error (KindInternal).
func TestNewProviderPublisherCollector_NilProvider(t *testing.T) {
	t.Parallel()
	_, err := NewProviderPublisherCollector(nil, "testcell")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind)
}

// TestNewProviderPublisherCollector_EmptyCellID verifies empty cellID returns
// an errcode.Error (KindInternal).
func TestNewProviderPublisherCollector_EmptyCellID(t *testing.T) {
	t.Parallel()
	_, err := NewProviderPublisherCollector(metrics.NopProvider{}, "")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind)
}

// TestNewProviderPublisherCollector_Registration verifies happy-path returns
// non-nil collector with no error.
func TestNewProviderPublisherCollector_Registration(t *testing.T) {
	t.Parallel()
	col, err := NewProviderPublisherCollector(metrics.NopProvider{}, "testcell")
	require.NoError(t, err)
	require.NotNil(t, col)
}

// TestProviderPublisherCollector_RecordPublishSuccess verifies that a success
// call increments mqtt_publish_total and observes mqtt_publish_ack_duration_seconds.
func TestProviderPublisherCollector_RecordPublishSuccess(t *testing.T) {
	t.Parallel()
	spy := newPubSpyProvider()
	col, err := NewProviderPublisherCollector(spy, "testcell")
	require.NoError(t, err)

	col.RecordPublishSuccess(context.Background(), testtime.D25ms)

	ops := spy.ops()
	// Expect: one Inc on publish_total and one Observe on ack_duration
	var foundTotal, foundDuration bool
	for _, op := range ops {
		switch {
		case op.name == "mqtt_publish_total" && op.op == "Inc":
			assert.Equal(t, "testcell", op.labels["cell"])
			foundTotal = true
		case op.name == "mqtt_publish_ack_duration_seconds" && op.op == "Observe":
			assert.Equal(t, "testcell", op.labels["cell"])
			assert.InDelta(t, 0.025, op.value, 1e-9)
			foundDuration = true
		}
	}
	assert.True(t, foundTotal, "mqtt_publish_total Inc not found in spy ops")
	assert.True(t, foundDuration, "mqtt_publish_ack_duration_seconds Observe not found in spy ops")
}

// TestProviderPublisherCollector_RecordPublishFailure verifies all 10 reason
// consts increment mqtt_publish_failed_total with the correct reason label.
func TestProviderPublisherCollector_RecordPublishFailure(t *testing.T) {
	t.Parallel()
	allReasons := []PublishFailureReason{
		PublishFailurePayloadTooLarge,
		PublishFailureClosed,
		PublishFailurePubAckTimeout,
		PublishFailureContextCanceled,
		PublishFailurePublishError,
		PublishFailureTopicOutsideNamespace,
		PublishFailureRateLimited,
		PublishFailureNotAuthorized,
		PublishFailurePayloadFormatInvalid,
		PublishFailureRejected,
	}
	for _, reason := range allReasons {
		reason := reason
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			spy := newPubSpyProvider()
			col, err := NewProviderPublisherCollector(spy, "testcell")
			require.NoError(t, err)

			col.RecordPublishFailure(context.Background(), reason)

			ops := spy.ops()
			var found bool
			for _, op := range ops {
				if op.name == "mqtt_publish_failed_total" && op.op == "Inc" {
					assert.Equal(t, "testcell", op.labels["cell"])
					assert.Equal(t, string(reason), op.labels["reason"])
					found = true
				}
			}
			assert.True(t, found, "mqtt_publish_failed_total Inc not found for reason %s", reason)
		})
	}
}

// TestNoopPublisherCollector_Methods verifies NoopPublisherCollector does not panic.
func TestNoopPublisherCollector_Methods(t *testing.T) {
	t.Parallel()
	col := NoopPublisherCollector{}
	assert.NotPanics(t, func() {
		col.RecordPublishSuccess(context.Background(), testtime.D10ms)
		col.RecordPublishFailure(context.Background(), PublishFailureClosed)
	})
}

// ---------------------------------------------------------------------------
// Publisher test doubles
// ---------------------------------------------------------------------------

type pubSpyRecord struct {
	name   string
	op     string
	labels metrics.Labels
	value  float64
}

type pubSpyProvider struct {
	counterRegs   []metrics.CounterOpts
	histogramRegs []metrics.HistogramOpts
	records       []pubSpyRecord
}

func newPubSpyProvider() *pubSpyProvider { return &pubSpyProvider{} }

func (p *pubSpyProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	p.counterRegs = append(p.counterRegs, opts)
	return &pubSpyCounterVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *pubSpyProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	p.histogramRegs = append(p.histogramRegs, opts)
	return &pubSpyHistogramVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *pubSpyProvider) GaugeVec(_ metrics.GaugeOpts) (metrics.GaugeVec, error) {
	return metrics.NopProvider{}.GaugeVec(metrics.GaugeOpts{})
}

func (p *pubSpyProvider) Unregister(_ metrics.Collector) error { return nil }

func (p *pubSpyProvider) ops() []pubSpyRecord {
	out := make([]pubSpyRecord, len(p.records))
	copy(out, p.records)
	return out
}

type pubSpyCounterVec struct {
	parent     *pubSpyProvider
	name       string
	labelNames []string
}

func (v *pubSpyCounterVec) Registered() bool { return true }

func (v *pubSpyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labelNames, l)
	return &pubSpyCounter{parent: v.parent, name: v.name, labels: l}
}

type pubSpyCounter struct {
	parent *pubSpyProvider
	name   string
	labels metrics.Labels
}

func (c *pubSpyCounter) Inc(_ context.Context) {
	c.parent.records = append(c.parent.records, pubSpyRecord{
		name: c.name, op: "Inc", labels: c.labels, value: 1,
	})
}

func (c *pubSpyCounter) Add(_ context.Context, d float64) {
	c.parent.records = append(c.parent.records, pubSpyRecord{
		name: c.name, op: "Add", labels: c.labels, value: d,
	})
}

type pubSpyHistogramVec struct {
	parent     *pubSpyProvider
	name       string
	labelNames []string
}

func (v *pubSpyHistogramVec) Registered() bool { return true }

func (v *pubSpyHistogramVec) With(l metrics.Labels) metrics.Histogram {
	metrics.MustValidateLabels(v.labelNames, l)
	return &pubSpyHistogram{parent: v.parent, name: v.name, labels: l}
}

type pubSpyHistogram struct {
	parent *pubSpyProvider
	name   string
	labels metrics.Labels
}

func (h *pubSpyHistogram) Observe(_ context.Context, val float64) {
	h.parent.records = append(h.parent.records, pubSpyRecord{
		name: h.name, op: "Observe", labels: h.labels, value: val,
	})
}
