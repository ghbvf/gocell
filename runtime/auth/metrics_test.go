package auth

import (
	"fmt"
	"sync"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// ─── lockout spy provider ──────────────────────────────────────────────────────

// lockoutSpyCounterVec records each (reason) value observed via Inc().
// Used exclusively by the AccountLockoutMetrics unit tests.
type lockoutSpyCounterVec struct {
	mu      sync.Mutex
	labels  []string
	byLabel map[string]int
}

func newLockoutSpyCounterVec() *lockoutSpyCounterVec {
	return &lockoutSpyCounterVec{
		labels:  []string{"reason"},
		byLabel: map[string]int{},
	}
}

func (v *lockoutSpyCounterVec) Registered() bool { return true }

func (v *lockoutSpyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labels, l)
	return &lockoutSpyCounter{vec: v, reason: l["reason"]}
}

func (v *lockoutSpyCounterVec) count(reason string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.byLabel[reason]
}

type lockoutSpyCounter struct {
	vec    *lockoutSpyCounterVec
	reason string
}

func (c *lockoutSpyCounter) Inc() {
	c.vec.mu.Lock()
	defer c.vec.mu.Unlock()
	c.vec.byLabel[c.reason]++
}
func (c *lockoutSpyCounter) Add(_ float64) {}

// lockoutSpyProvider wraps metrics.NopProvider but intercepts
// auth_account_lockout_total registrations, returning the spy vec.
type lockoutSpyProvider struct {
	lockoutVec *lockoutSpyCounterVec
}

func newLockoutSpyProvider() *lockoutSpyProvider {
	return &lockoutSpyProvider{lockoutVec: newLockoutSpyCounterVec()}
}

func (p *lockoutSpyProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	if opts.Name == "auth_account_lockout_total" {
		return p.lockoutVec, nil
	}
	return metrics.NopProvider{}.CounterVec(opts)
}

func (p *lockoutSpyProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return metrics.NopProvider{}.HistogramVec(opts)
}

func (p *lockoutSpyProvider) GaugeVec(opts metrics.GaugeOpts) (metrics.GaugeVec, error) {
	return metrics.NopProvider{}.GaugeVec(opts)
}

func (p *lockoutSpyProvider) Unregister(_ metrics.Collector) error { return nil }

func TestNewAuthMetrics_NopProvider(t *testing.T) {
	am, err := NewAuthMetrics(metrics.NopProvider{})
	require.NoError(t, err)
	require.NotNil(t, am)
}

func TestNewAuthMetrics_NilProvider(t *testing.T) {
	_, err := NewAuthMetrics(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil")
}

func TestAuthMetrics_RecordTokenVerify_NoPanic(t *testing.T) {
	am, err := NewAuthMetrics(metrics.NopProvider{})
	require.NoError(t, err)
	// Should not panic with valid labels.
	am.recordTokenVerify("success", "ok", testtime.FastPoll)
	am.recordTokenVerify("failure", "expired", testtime.D1ms)
}

func TestAuthMetrics_RecordServiceVerify_NoPanic(t *testing.T) {
	am, err := NewAuthMetrics(metrics.NopProvider{})
	require.NoError(t, err)
	am.recordServiceVerify("success", "ok")
	am.recordServiceVerify("failure", "expired")
}

func TestAuthMetrics_NilSafe(t *testing.T) {
	// nil AuthMetrics should not panic.
	var am *AuthMetrics
	am.recordTokenVerify("success", "ok", testtime.D1ms)
	am.recordServiceVerify("success", "ok")
}

// ─── AccountLockoutMetrics tests (F6) ────────────────────────────────────────

// TestNewAccountLockoutMetrics_NilProvider verifies that a nil provider is
// rejected with an error at construction time.
func TestNewAccountLockoutMetrics_NilProvider(t *testing.T) {
	_, err := NewAccountLockoutMetrics(nil)
	require.Error(t, err, "nil provider must be rejected")
	assert.Contains(t, err.Error(), "must not be nil")
}

// TestNewAccountLockoutMetrics_NopProvider verifies that a valid (NopProvider)
// succeeds and returns a non-nil *AccountLockoutMetrics.
func TestNewAccountLockoutMetrics_NopProvider(t *testing.T) {
	m, err := NewAccountLockoutMetrics(metrics.NopProvider{})
	require.NoError(t, err)
	require.NotNil(t, m, "NewAccountLockoutMetrics must return non-nil on success")
}

// TestAccountLockoutMetrics_NilSafe verifies that calling IncAccountLockout on a
// nil *AccountLockoutMetrics does not panic. This is the nil-receiver guard used
// when the composition root omits metrics wiring (e.g., tests).
func TestAccountLockoutMetrics_NilSafe(t *testing.T) {
	var m *AccountLockoutMetrics
	require.NotPanics(t, func() {
		m.IncAccountLockout("threshold_locked")
	}, "nil *AccountLockoutMetrics.IncAccountLockout must not panic")
}

// TestAccountLockoutMetrics_IncAccountLockout_EmitsLabel verifies that
// IncAccountLockout records the correct reason label in the underlying
// CounterVec for both recognized reason values.
func TestAccountLockoutMetrics_IncAccountLockout_EmitsLabel(t *testing.T) {
	spy := newLockoutSpyProvider()
	m, err := NewAccountLockoutMetrics(spy)
	require.NoError(t, err)

	m.IncAccountLockout("threshold_locked")
	m.IncAccountLockout("threshold_locked")
	m.IncAccountLockout("lazy_unlocked")

	assert.Equal(t, 2, spy.lockoutVec.count("threshold_locked"),
		`IncAccountLockout("threshold_locked") must increment the "threshold_locked" label`)
	assert.Equal(t, 1, spy.lockoutVec.count("lazy_unlocked"),
		`IncAccountLockout("lazy_unlocked") must increment the "lazy_unlocked" label`)
	assert.Equal(t, 0, spy.lockoutVec.count("other"),
		"unrecorded reason must have count 0")
}

func TestClassifyTokenError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "ok"},
		{"jwt expired", jwt.ErrTokenExpired, "expired"},
		{"jwt not valid yet", jwt.ErrTokenNotValidYet, "expired"},
		{"jwt invalid signature", jwt.ErrTokenSignatureInvalid, "invalid_signature"},
		{"kid error", fmt.Errorf("token verification failed: missing kid header"), "invalid_kid"},
		{"wrong alg", fmt.Errorf("token verification failed: unexpected signing method: HS256"), "wrong_alg"},
		{"other", fmt.Errorf("something unexpected"), "invalid_token"},
		{"invalid_intent", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidTokenIntent, "x"), "invalid_intent"},
		// Verifier-side infra failures must bucket into a dedicated reason so
		// SLO dashboards can separate "credential failures" from "auth
		// dependency degraded" (Finding #3 PR #490 second review).
		{
			"kind_unavailable",
			errcode.New(errcode.KindUnavailable, errcode.ErrAuthServiceUnavailable, "service unavailable"),
			"service_unavailable",
		},
		{
			"category_infra",
			errcode.New(errcode.KindInternal, errcode.ErrInternal, "kms outage",
				errcode.WithCategory(errcode.CategoryInfra)),
			"service_unavailable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyTokenError(tt.err))
		})
	}
}
