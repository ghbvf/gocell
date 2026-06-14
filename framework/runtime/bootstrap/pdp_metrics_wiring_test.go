package bootstrap

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/authz"
)

// pdpWireFakeAuthorizer is a minimal auth.Authorizer used to drive the primary
// authorizer injector path. It does NOT implement ResolveAuthorizer, so the
// startup eager-resolve step is skipped (nothing to resolve).
type pdpWireFakeAuthorizer struct{}

func (pdpWireFakeAuthorizer) Authorize(context.Context, string, string, string) (authz.Decision, error) {
	return authz.Deny("test"), nil
}

// pdpWireResolvingAuthorizer implements both Authorize and ResolveAuthorizer so
// the test can assert resolve runs (and on the bare authorizer, before the
// observableAuthorizer wrap which does NOT implement ResolveAuthorizer).
type pdpWireResolvingAuthorizer struct {
	resolveErr error
	resolved   bool
}

func (a *pdpWireResolvingAuthorizer) Authorize(context.Context, string, string, string) (authz.Decision, error) {
	return authz.Deny("test"), nil
}

func (a *pdpWireResolvingAuthorizer) ResolveAuthorizer() error {
	a.resolved = true
	return a.resolveErr
}

// TestAppendPrimaryAuthorizerInjector_RegistersPDPMetrics is the #2027 wiring
// guard: when a metrics Provider AND a primary Authorizer are present, the
// injector path registers the PDP decision instruments (the observableAuthorizer
// decorator wraps the PDP). Registration of both metric names proves NewPDPMetrics
// ran on the injected Provider.
func TestAppendPrimaryAuthorizerInjector_RegistersPDPMetrics(t *testing.T) {
	spy := &registrationSpy{}
	b := New(clock.Real(), WithMetricsProvider(spy))
	b.primaryAuthorizer = pdpWireFakeAuthorizer{}

	opts, err := b.appendPrimaryAuthorizerInjector(nil)
	require.NoError(t, err)
	require.Len(t, opts, 1, "authorizer injector middleware must be appended")

	assert.True(t, slices.Contains(spy.counters(), "auth_pdp_decision_total"),
		"PDP decision counter must register when provider+authorizer present, got %v", spy.counters())
	assert.True(t, slices.Contains(spy.histograms(), "auth_pdp_decision_duration_seconds"),
		"PDP decision duration histogram must register, got %v", spy.histograms())
}

// TestPDPAuthorizerForInjection_RealProvider_Wraps: a real metrics provider →
// the authorizer is wrapped in observableAuthorizer and PDP metrics register.
func TestPDPAuthorizerForInjection_RealProvider_Wraps(t *testing.T) {
	spy := &registrationSpy{}
	b := New(clock.Real(), WithMetricsProvider(spy))
	fake := pdpWireFakeAuthorizer{}

	got, err := b.pdpAuthorizerForInjection(fake)
	require.NoError(t, err)
	assert.NotEqual(t, fake, got, "a real metrics provider must wrap the authorizer in observableAuthorizer")
	assert.True(t, slices.Contains(spy.counters(), "auth_pdp_decision_total"),
		"real provider must register the PDP counter, got %v", spy.counters())
	assert.True(t, slices.Contains(spy.histograms(), "auth_pdp_decision_duration_seconds"),
		"real provider must register the PDP histogram, got %v", spy.histograms())
}

// TestPDPAuthorizerForInjection_NopProvider_NoWrap is the #2077 F3 guard:
// b.metricsProvider defaults to a NopProvider (non-nil), so PDP wiring must gate on
// hasRealMetricsProvider — NOT a `!= nil` check — and return the BARE authorizer
// unwrapped (no no-op PDPMetrics, no observableAuthorizer) when no real backend is
// wired, staying on the same metric-autowire funnel as every other collector.
func TestPDPAuthorizerForInjection_NopProvider_NoWrap(t *testing.T) {
	b := New(clock.Real()) // metricsProvider defaults to NopProvider{}
	fake := pdpWireFakeAuthorizer{}

	got, err := b.pdpAuthorizerForInjection(fake)
	require.NoError(t, err)
	assert.Equal(t, fake, got,
		"default NopProvider must NOT wrap — the bare authorizer is injected (PR #2077 F3)")
}

// TestAppendPrimaryAuthorizerInjector_NoProvider_StillInstallsInjector: without a
// real provider the injector middleware is still appended (the bare authorizer) —
// metrics absence never blocks authorization wiring.
func TestAppendPrimaryAuthorizerInjector_NoProvider_StillInstallsInjector(t *testing.T) {
	b := New(clock.Real()) // no metrics provider
	b.primaryAuthorizer = pdpWireFakeAuthorizer{}

	opts, err := b.appendPrimaryAuthorizerInjector(nil)
	require.NoError(t, err)
	require.Len(t, opts, 1, "injector still installed without a metrics provider (fail-open)")
}

// TestAppendPrimaryAuthorizerInjector_ResolvesBeforeWrapping locks the ordering
// invariant: ResolveAuthorizer must run on the bare primary Authorizer BEFORE it
// is wrapped in observableAuthorizer (which does not implement ResolveAuthorizer).
// A resolve error must bubble (proving resolve ran), and PDP metrics must NOT
// register because we bail before the wrap. If a future refactor wrapped first,
// the resolve would be silently skipped and this test would fail.
func TestAppendPrimaryAuthorizerInjector_ResolvesBeforeWrapping(t *testing.T) {
	sentinel := errors.New("resolve boom")
	fake := &pdpWireResolvingAuthorizer{resolveErr: sentinel}
	spy := &registrationSpy{}
	b := New(clock.Real(), WithMetricsProvider(spy))
	b.primaryAuthorizer = fake

	_, err := b.appendPrimaryAuthorizerInjector(nil)
	require.Error(t, err)
	require.ErrorIs(t, err, sentinel,
		"ResolveAuthorizer error must bubble — resolve runs on the bare authorizer before wrapping")
	require.True(t, fake.resolved, "ResolveAuthorizer must have been invoked")
	assert.NotContains(t, spy.counters(), "auth_pdp_decision_total",
		"PDP metrics must NOT register when resolve fails before the wrap")
}
