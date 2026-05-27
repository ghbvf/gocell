package mqtt

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
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
