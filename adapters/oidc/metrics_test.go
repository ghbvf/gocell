package oidc

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestNewProviderRefreshCollector_RejectsEmptyCellID verifies that the
// constructor fails fast with ErrObservabilityConfigInvalid when cellID is
// empty — the cell label is part of the metric series identity.
func TestNewProviderRefreshCollector_RejectsEmptyCellID(t *testing.T) {
	t.Parallel()
	_, err := NewProviderRefreshCollector(metrics.NopProvider{}, "")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrObservabilityConfigInvalid, ec.Code)
}

// TestNewProviderRefreshCollector_RejectsNilProvider guards against silent
// metric drops when wiring forgets to inject a Provider.
func TestNewProviderRefreshCollector_RejectsNilProvider(t *testing.T) {
	t.Parallel()
	_, err := NewProviderRefreshCollector(nil, "oidctest")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrObservabilityConfigInvalid, ec.Code)
}

// TestNewProviderRefreshCollector_NopProviderNoPanic exercises the happy path
// on the no-op provider: RecordRefresh(true) and RecordRefresh(false) must
// not panic and the constructor must return a usable RefreshCollector.
func TestNewProviderRefreshCollector_NopProviderNoPanic(t *testing.T) {
	t.Parallel()
	c, err := NewProviderRefreshCollector(metrics.NopProvider{}, "oidctest")
	require.NoError(t, err)
	require.NotNil(t, c)

	assert.NotPanics(t, func() { c.RecordRefresh(true) })
	assert.NotPanics(t, func() { c.RecordRefresh(false) })
}

// TestNewProviderRefreshCollector_RegistrationFailure verifies that a Provider
// returning an error from CounterVec is wrapped with the configured errcode so
// callers can fail-fast at composition root.
func TestNewProviderRefreshCollector_RegistrationFailure(t *testing.T) {
	t.Parallel()
	provider := &refreshErrProvider{err: errors.New("duplicate counter: oidc_jwks_refresh_total")}
	_, err := NewProviderRefreshCollector(provider, "oidctest")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrObservabilityConfigInvalid, ec.Code)
}

// TestProviderRefreshCollector_RecordRefresh_Labels confirms that success and
// failure map to the correct {cell, result} label pair. This pins the metric
// schema so future refactors surface as a test failure, not a silent mismatch.
func TestProviderRefreshCollector_RecordRefresh_Labels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		success    bool
		wantResult string
	}{
		{true, "success"},
		{false, "failure"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.wantResult, func(t *testing.T) {
			t.Parallel()
			provider := newRefreshSpyProvider()
			col, err := NewProviderRefreshCollector(provider, "oidctest")
			require.NoError(t, err)

			col.RecordRefresh(tc.success)

			ops := provider.ops()
			require.Len(t, ops, 1, "exactly one counter Inc per RecordRefresh")
			assert.Equal(t, "oidc_jwks_refresh_total", ops[0].name)
			assert.Equal(t, "Inc", ops[0].op)
			assert.Equal(t, "oidctest", ops[0].labels["cell"])
			assert.Equal(t, tc.wantResult, ops[0].labels["result"])
		})
	}
}

// TestProviderRefreshCollector_RegistersExpectedLabelNames pins the label
// schema so an adapter refactor cannot silently change label names.
func TestProviderRefreshCollector_RegistersExpectedLabelNames(t *testing.T) {
	t.Parallel()
	provider := newRefreshSpyProvider()
	_, err := NewProviderRefreshCollector(provider, "oidctest")
	require.NoError(t, err)

	registrations := provider.registrations()
	require.Len(t, registrations, 1)
	assert.Equal(t, "oidc_jwks_refresh_total", registrations[0].Name)
	assert.Equal(t, []string{"cell", "result"}, registrations[0].LabelNames)
}

// TestNoopRefreshCollector_NoPanic verifies the noop impl never panics.
func TestNoopRefreshCollector_NoPanic(t *testing.T) {
	t.Parallel()
	var c NoopRefreshCollector
	assert.NotPanics(t, func() { c.RecordRefresh(true) })
	assert.NotPanics(t, func() { c.RecordRefresh(false) })
}

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// refreshErrProvider returns a fixed error from CounterVec to exercise the
// registration-failure path of NewProviderRefreshCollector.
type refreshErrProvider struct{ err error }

func (p *refreshErrProvider) CounterVec(_ metrics.CounterOpts) (metrics.CounterVec, error) {
	return nil, p.err
}

func (p *refreshErrProvider) HistogramVec(_ metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return nil, p.err
}

func (p *refreshErrProvider) Unregister(_ metrics.Collector) error { return nil }

// refreshSpyProvider records counter registrations and Inc/Add operations so
// tests can assert exact metric emissions without a real backend.
type refreshSpyProvider struct {
	regs    []metrics.CounterOpts
	records []refreshSpyRecord
}

type refreshSpyRecord struct {
	name   string
	op     string
	labels metrics.Labels
	value  float64
}

func newRefreshSpyProvider() *refreshSpyProvider {
	return &refreshSpyProvider{}
}

func (p *refreshSpyProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	p.regs = append(p.regs, opts)
	return &refreshSpyCounterVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *refreshSpyProvider) HistogramVec(_ metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return nil, errors.New("refreshSpyProvider: HistogramVec not implemented")
}

func (p *refreshSpyProvider) Unregister(_ metrics.Collector) error { return nil }

func (p *refreshSpyProvider) ops() []refreshSpyRecord {
	out := make([]refreshSpyRecord, len(p.records))
	copy(out, p.records)
	return out
}

func (p *refreshSpyProvider) registrations() []metrics.CounterOpts {
	out := make([]metrics.CounterOpts, len(p.regs))
	copy(out, p.regs)
	return out
}

type refreshSpyCounterVec struct {
	parent     *refreshSpyProvider
	name       string
	labelNames []string
}

func (v *refreshSpyCounterVec) Registered() bool { return true }

func (v *refreshSpyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labelNames, l)
	return &refreshSpyCounter{parent: v.parent, name: v.name, labels: l}
}

type refreshSpyCounter struct {
	parent *refreshSpyProvider
	name   string
	labels metrics.Labels
}

func (c *refreshSpyCounter) Inc() {
	c.parent.records = append(c.parent.records, refreshSpyRecord{
		name: c.name, op: "Inc", labels: c.labels, value: 1,
	})
}

func (c *refreshSpyCounter) Add(d float64) {
	c.parent.records = append(c.parent.records, refreshSpyRecord{
		name: c.name, op: "Add", labels: c.labels, value: d,
	})
}
