package interceptor

// chain_pdp_metrics_test.go — #2008 F8: NewServerInterceptors wraps the PDP
// Authorizer with decision metrics when (and only when) a real metrics provider is
// configured AND there is an Authorizer to gate. The wrap registers the shared
// auth_pdp_decision_* family at construction, so it is observable without RPCs.

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// recordingProvider is a real (non-Nop) metrics provider that records which counter
// families get registered, so the F8 wrap is observable. It embeds NopProvider for
// the unrecorded methods; its dynamic type is *recordingProvider, so kernelmetrics.IsReal
// treats it as a real provider.
type recordingProvider struct {
	kernelmetrics.NopProvider
	mu       sync.Mutex
	counters []string
}

func (p *recordingProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	p.mu.Lock()
	p.counters = append(p.counters, opts.Name)
	p.mu.Unlock()
	return p.NopProvider.CounterVec(opts)
}

func (p *recordingProvider) registered(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range p.counters {
		if n == name {
			return true
		}
	}
	return false
}

func pdpMetricsDeps(authorizer auth.Authorizer, prov kernelmetrics.Provider) Deps {
	return Deps{
		Clock:           clock.Real(),
		Verifier:        stubVerifier{},
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Authorizer:      authorizer,
		MetricsProvider: prov,
		CellIDClosedSet: []string{"c"},
	}
}

// TestNewServerInterceptors_RealProvider_WrapsAuthorizerWithPDPMetrics asserts F8:
// a real metrics provider + an Authorizer triggers the observable-authorizer wrap,
// registering the shared auth_pdp_decision_total family so gRPC PDP decisions reach
// the same series as HTTP.
func TestNewServerInterceptors_RealProvider_WrapsAuthorizerWithPDPMetrics(t *testing.T) {
	t.Parallel()
	prov := &recordingProvider{}
	_ = NewServerInterceptors(pdpMetricsDeps(stubAuthorizer{dec: mustAllow()}, prov))
	assert.True(t, prov.registered("auth_pdp_decision_total"),
		"a real metrics provider must trigger the gRPC PDP-metrics wrap")
}

// TestNewServerInterceptors_NoAuthorizer_NoPDPMetricsWrap asserts the wrap is skipped
// when there is no Authorizer to wrap (nothing to gate), even with a real provider —
// so a public-only / ungated server registers no PDP metrics.
func TestNewServerInterceptors_NoAuthorizer_NoPDPMetricsWrap(t *testing.T) {
	t.Parallel()
	prov := &recordingProvider{}
	_ = NewServerInterceptors(pdpMetricsDeps(nil, prov))
	assert.False(t, prov.registered("auth_pdp_decision_total"),
		"no Authorizer means nothing to wrap; PDP metrics must not be registered")
}

// TestNewServerInterceptors_NopProvider_BuildsWithoutWrap asserts a Nop provider
// (IsReal==false) leaves the Authorizer bare — metrics are best-effort, never gate
// the verdict — and construction still yields a usable bundle.
func TestNewServerInterceptors_NopProvider_BuildsWithoutWrap(t *testing.T) {
	t.Parallel()
	b := NewServerInterceptors(pdpMetricsDeps(stubAuthorizer{dec: mustAllow()}, kernelmetrics.NopProvider{}))
	assert.NotNil(t, b.Registrar(), "construction with a Nop provider must still yield a bundle")
}
