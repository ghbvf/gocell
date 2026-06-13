package bootstrap

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/authz"
)

// pdpWireFakeAuthorizer is a minimal auth.Authorizer used to drive the primary
// authorizer injector path. It does NOT implement ResolveAuthorizer, so the
// startup eager-resolve step is skipped (nothing to resolve).
type pdpWireFakeAuthorizer struct{}

func (pdpWireFakeAuthorizer) Authorize(context.Context, string, string, string) (authz.Decision, error) {
	return authz.Deny("test"), nil
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

// TestAppendPrimaryAuthorizerInjector_NoProvider_NoPDPMetrics pins the fail-open
// inverse: without a metrics Provider the injector is still installed (the bare
// Authorizer), and no PDP metrics are registered — metrics absence never blocks
// authorization wiring.
func TestAppendPrimaryAuthorizerInjector_NoProvider_NoPDPMetrics(t *testing.T) {
	b := New(clock.Real()) // no metrics provider
	b.primaryAuthorizer = pdpWireFakeAuthorizer{}

	opts, err := b.appendPrimaryAuthorizerInjector(nil)
	require.NoError(t, err)
	require.Len(t, opts, 1, "injector still installed without a metrics provider (fail-open)")
}
