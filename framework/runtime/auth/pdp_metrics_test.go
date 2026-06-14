package auth

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// ─── PDP decision spy provider ──────────────────────────────────────────────
//
// Captures the (action, decision) tuples reaching auth_pdp_decision_total and
// the (decision) buckets reaching auth_pdp_decision_duration_seconds, so the
// decorator tests can assert the closed-set labels are emitted correctly.

type pdpSpyProvider struct {
	counter   *pdpSpyCounterVec
	histogram *pdpSpyHistogramVec
}

func newPDPSpyProvider() *pdpSpyProvider {
	return &pdpSpyProvider{
		counter:   &pdpSpyCounterVec{labels: []string{"action", "decision"}, byTuple: map[string]int{}},
		histogram: &pdpSpyHistogramVec{labels: []string{"decision"}, byLabel: map[string]int{}},
	}
}

func (p *pdpSpyProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	if opts.Name == "auth_pdp_decision_total" {
		return p.counter, nil
	}
	return metrics.NopProvider{}.CounterVec(opts)
}

func (p *pdpSpyProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	if opts.Name == "auth_pdp_decision_duration_seconds" {
		return p.histogram, nil
	}
	return metrics.NopProvider{}.HistogramVec(opts)
}

func (p *pdpSpyProvider) GaugeVec(opts metrics.GaugeOpts) (metrics.GaugeVec, error) {
	return metrics.NopProvider{}.GaugeVec(opts)
}

func (p *pdpSpyProvider) Unregister(_ metrics.Collector) error { return nil }

type pdpSpyCounterVec struct {
	mu      sync.Mutex
	labels  []string
	byTuple map[string]int
}

func (v *pdpSpyCounterVec) Registered() bool { return true }

func (v *pdpSpyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labels, l)
	return &pdpSpyCounter{vec: v, key: l["action"] + "|" + l["decision"]}
}

func (v *pdpSpyCounterVec) count(action, decision string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.byTuple[action+"|"+decision]
}

type pdpSpyCounter struct {
	vec *pdpSpyCounterVec
	key string
}

func (c *pdpSpyCounter) Inc(_ context.Context) {
	c.vec.mu.Lock()
	defer c.vec.mu.Unlock()
	c.vec.byTuple[c.key]++
}
func (c *pdpSpyCounter) Add(_ context.Context, _ float64) {}

type pdpSpyHistogramVec struct {
	mu      sync.Mutex
	labels  []string
	byLabel map[string]int
}

func (v *pdpSpyHistogramVec) Registered() bool { return true }

func (v *pdpSpyHistogramVec) With(l metrics.Labels) metrics.Histogram {
	metrics.MustValidateLabels(v.labels, l)
	return &pdpSpyHistogram{vec: v, decision: l["decision"]}
}

func (v *pdpSpyHistogramVec) observations(decision string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.byLabel[decision]
}

type pdpSpyHistogram struct {
	vec      *pdpSpyHistogramVec
	decision string
}

func (h *pdpSpyHistogram) Observe(_ context.Context, _ float64) {
	h.vec.mu.Lock()
	defer h.vec.mu.Unlock()
	h.vec.byLabel[h.decision]++
}

// ─── fake inner Authorizer ──────────────────────────────────────────────────

type pdpFakeAuthorizer struct {
	dec authz.Decision
	err error
}

func (f pdpFakeAuthorizer) Authorize(_ context.Context, _, _, _ string) (authz.Decision, error) {
	return f.dec, f.err
}

func mustAllow(t *testing.T) authz.Decision {
	t.Helper()
	dec, err := authz.Allow(authz.Obligations{})
	require.NoError(t, err)
	return dec
}

// ─── constructor guards ─────────────────────────────────────────────────────

func TestNewPDPMetrics_NilProvider(t *testing.T) {
	_, err := NewPDPMetrics(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil")
}

func TestNewPDPMetrics_NopProvider(t *testing.T) {
	m, err := NewPDPMetrics(metrics.NopProvider{})
	require.NoError(t, err)
	require.NotNil(t, m)
}

func TestPDPMetrics_NilSafe(t *testing.T) {
	var m *PDPMetrics
	require.NotPanics(t, func() {
		m.recordDecision(context.Background(), authz.PermAuditRead().String(), pdpDecisionAllow, 0)
	}, "nil *PDPMetrics.recordDecision must not panic (best-effort, fail-open)")
}

// ─── observableAuthorizer: decision classification + label emission ──────────

func TestObservableAuthorizer_RecordsDecision(t *testing.T) {
	const action = "audit:read"
	// F2 (#2077): `error` is infra-only. A store-unavailable (KindUnavailable → 503)
	// error and an unexpected/unclassified plain error are `error`; a policy /
	// auth-context denial returned as KindPermissionDenied (403) / KindUnauthenticated
	// (401) is `deny`, NOT `error` — so the critical store-error alert never
	// false-pages on ordinary client auth failures.
	errStore := errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "store down")
	errTenantMissing := errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "tenant scope missing")
	errNoPrincipal := errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "no principal")
	tests := []struct {
		name         string
		inner        pdpFakeAuthorizer
		wantDecision string
	}{
		{"allow", pdpFakeAuthorizer{dec: mustAllow(t)}, "allow"},
		{"deny", pdpFakeAuthorizer{dec: authz.Deny("nope")}, "deny"},
		{"store-unavailable → error", pdpFakeAuthorizer{err: errStore}, "error"},
		{"unexpected plain error → error", pdpFakeAuthorizer{err: errors.New("boom")}, "error"},
		{"tenant-missing 403 → deny", pdpFakeAuthorizer{err: errTenantMissing}, "deny"},
		{"no-principal 401 → deny", pdpFakeAuthorizer{err: errNoPrincipal}, "deny"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := newPDPSpyProvider()
			m, err := NewPDPMetrics(spy)
			require.NoError(t, err)

			oa := NewObservableAuthorizer(clock.Real(), tt.inner, m)
			gotDec, gotErr := oa.Authorize(context.Background(), "sub", "res", action)

			// Decision + error are passed through unchanged.
			assert.Equal(t, tt.inner.dec.IsAllow(), gotDec.IsAllow(), "decision must pass through unchanged")
			if tt.inner.err != nil {
				assert.Error(t, gotErr)
			} else {
				assert.NoError(t, gotErr)
			}

			// The classified decision label is recorded on both instruments.
			assert.Equal(t, 1, spy.counter.count(action, tt.wantDecision),
				"counter must record action=%q decision=%q exactly once", action, tt.wantDecision)
			assert.Equal(t, 1, spy.histogram.observations(tt.wantDecision),
				"histogram must record one observation for decision=%q", tt.wantDecision)
		})
	}
}

// TestObservableAuthorizer_NilMetrics_FailOpen proves a nil recorder never
// blocks or panics: metrics are best-effort and must never affect the verdict.
func TestObservableAuthorizer_NilMetrics_FailOpen(t *testing.T) {
	oa := NewObservableAuthorizer(clock.Real(), pdpFakeAuthorizer{dec: authz.Deny("x")}, nil)
	require.NotPanics(t, func() {
		dec, err := oa.Authorize(context.Background(), "s", "r", "a")
		assert.NoError(t, err)
		assert.False(t, dec.IsAllow())
	})
}

// TestNewObservableAuthorizer_NilClock_Panics pins the clock.MustHaveClock guard
// (clock is a mandatory positional dependency, per go-standards).
func TestNewObservableAuthorizer_NilClock_Panics(t *testing.T) {
	var clk clock.Clock // typed nil
	require.Panics(t, func() {
		_ = NewObservableAuthorizer(clk, pdpFakeAuthorizer{}, nil)
	})
}

// TestNewObservableAuthorizer_NilInner_Panics pins the inner fail-fast guard:
// a nil Authorizer must panic at construction rather than nil-deref on the first
// request (强依赖 fail-fast, mirroring the clock guard). Covers both bare and
// typed-nil interface forms.
func TestNewObservableAuthorizer_NilInner_Panics(t *testing.T) {
	t.Run("bare nil", func(t *testing.T) {
		require.Panics(t, func() {
			_ = NewObservableAuthorizer(clock.Real(), nil, nil)
		})
	})
	t.Run("typed nil", func(t *testing.T) {
		var inner *observableAuthorizer // typed-nil Authorizer
		require.Panics(t, func() {
			_ = NewObservableAuthorizer(clock.Real(), inner, nil)
		})
	})
}
