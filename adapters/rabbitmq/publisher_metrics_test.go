package rabbitmq

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestNewProviderPublisherCollector_RejectsEmptyCellID verifies the constructor
// fails fast when called without a cell identifier — the cell label is part
// of the metric series identity, so an empty value would degrade the
// alerting series into a single anonymous bucket.
func TestNewProviderPublisherCollector_RejectsEmptyCellID(t *testing.T) {
	t.Parallel()
	_, err := NewProviderPublisherCollector(metrics.NopProvider{}, "")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrObservabilityConfigInvalid, ec.Code)
}

// TestNewProviderPublisherCollector_RejectsNilProvider guards against silent
// metric drops when wiring forgets to inject a Provider.
func TestNewProviderPublisherCollector_RejectsNilProvider(t *testing.T) {
	t.Parallel()
	_, err := NewProviderPublisherCollector(nil, "rmqtest")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrObservabilityConfigInvalid, ec.Code)
}

// TestNewProviderPublisherCollector_NopProviderNoPanic exercises the happy
// path on the no-op provider: every reason value must round-trip through
// RecordPublishFailure without panicking, and the constructor must return a
// usable PublisherCollector.
func TestNewProviderPublisherCollector_NopProviderNoPanic(t *testing.T) {
	t.Parallel()
	c, err := NewProviderPublisherCollector(metrics.NopProvider{}, "rmqtest")
	require.NoError(t, err)
	require.NotNil(t, c)

	for _, r := range allPublishFailureReasons() {
		assert.NotPanics(t, func() { c.RecordPublishFailure(r) })
	}
}

// TestNewProviderPublisherCollector_RegistrationFailure verifies that a
// Provider returning an error from CounterVec is wrapped with the
// configured errcode so callers can fail-fast at composition root.
func TestNewProviderPublisherCollector_RegistrationFailure(t *testing.T) {
	t.Parallel()
	provider := &errProvider{err: errors.New("duplicate counter: rabbitmq_publish_failed_total")}
	_, err := NewProviderPublisherCollector(provider, "rmqtest")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrObservabilityConfigInvalid, ec.Code)
}

// TestProviderPublisherCollector_RecordPublishFailure_AllReasons confirms that
// every PublishFailureReason maps to a single Counter.Inc with the correct
// {cell, reason} label pair. This locks down the closed reason set so future
// additions surface as a test failure, not a silent metric mismatch.
func TestProviderPublisherCollector_RecordPublishFailure_AllReasons(t *testing.T) {
	t.Parallel()

	reasons := allPublishFailureReasons()

	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			provider := newPublisherSpyProvider()
			col, err := NewProviderPublisherCollector(provider, "rmqtest")
			require.NoError(t, err)

			col.RecordPublishFailure(reason)

			ops := provider.ops()
			require.Len(t, ops, 1, "exactly one counter Inc per RecordPublishFailure")
			assert.Equal(t, "rabbitmq_publish_failed_total", ops[0].name)
			assert.Equal(t, "Inc", ops[0].op)
			assert.Equal(t, "rmqtest", ops[0].labels["cell"])
			assert.Equal(t, string(reason), ops[0].labels["reason"])
		})
	}
}

// TestProviderPublisherCollector_RegistersExpectedLabelNames pins the label
// schema so an adapter refactor cannot silently change `cell` or `reason`.
func TestProviderPublisherCollector_RegistersExpectedLabelNames(t *testing.T) {
	t.Parallel()
	provider := newPublisherSpyProvider()
	_, err := NewProviderPublisherCollector(provider, "rmqtest")
	require.NoError(t, err)

	registrations := provider.registrations()
	require.Len(t, registrations, 1)
	assert.Equal(t, "rabbitmq_publish_failed_total", registrations[0].Name)
	assert.Equal(t, []string{"cell", "reason"}, registrations[0].LabelNames)
}

// allPublishFailureReasons returns the closed reason set documented on
// PublishFailureReason. Returning a slice (not a map) keeps the test
// deterministic and lets t.Run subtests emit stable names.
func allPublishFailureReasons() []PublishFailureReason {
	return []PublishFailureReason{
		PublishFailureNack,
		PublishFailureTimeout,
		PublishFailureChanClosed,
		PublishFailureAcquireChannel,
		PublishFailureDeclareExchange,
		PublishFailureConfirmMode,
		PublishFailurePublishSend,
	}
}

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// errProvider returns a fixed error from CounterVec to exercise the
// registration-failure path of NewProviderPublisherCollector.
type errProvider struct{ err error }

func (p *errProvider) CounterVec(_ metrics.CounterOpts) (metrics.CounterVec, error) {
	return nil, p.err
}

func (p *errProvider) HistogramVec(_ metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return nil, p.err
}

func (p *errProvider) Unregister(_ metrics.Collector) error { return nil }

// publisherSpyProvider records counter registrations and Inc/Add operations so
// tests can assert exact metric emissions without depending on a real
// Prometheus or OTel backend.
type publisherSpyProvider struct {
	regs    []metrics.CounterOpts
	records []spyCounterRecord
}

type spyCounterRecord struct {
	name   string
	op     string
	labels metrics.Labels
	value  float64
}

func newPublisherSpyProvider() *publisherSpyProvider {
	return &publisherSpyProvider{}
}

func (p *publisherSpyProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	p.regs = append(p.regs, opts)
	return &publisherSpyCounterVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *publisherSpyProvider) HistogramVec(_ metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return nil, errors.New("publisherSpyProvider: HistogramVec not implemented")
}

func (p *publisherSpyProvider) Unregister(_ metrics.Collector) error { return nil }

func (p *publisherSpyProvider) ops() []spyCounterRecord {
	out := make([]spyCounterRecord, len(p.records))
	copy(out, p.records)
	return out
}

func (p *publisherSpyProvider) registrations() []metrics.CounterOpts {
	out := make([]metrics.CounterOpts, len(p.regs))
	copy(out, p.regs)
	return out
}

type publisherSpyCounterVec struct {
	parent     *publisherSpyProvider
	name       string
	labelNames []string
}

func (v *publisherSpyCounterVec) Registered() bool { return true }

func (v *publisherSpyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labelNames, l)
	return &publisherSpyCounter{parent: v.parent, name: v.name, labels: l}
}

type publisherSpyCounter struct {
	parent *publisherSpyProvider
	name   string
	labels metrics.Labels
}

func (c *publisherSpyCounter) Inc() {
	c.parent.records = append(c.parent.records, spyCounterRecord{
		name: c.name, op: "Inc", labels: c.labels, value: 1,
	})
}

func (c *publisherSpyCounter) Add(d float64) {
	c.parent.records = append(c.parent.records, spyCounterRecord{
		name: c.name, op: "Add", labels: c.labels, value: d,
	})
}
