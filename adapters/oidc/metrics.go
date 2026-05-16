package oidc

import (
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// RefreshCollector observes OIDC JWKS refresh attempts so alerting rules can
// detect stale discovery metadata without parsing log strings.
//
// Implementations must be safe for concurrent use.
//
// ref: adapters/rabbitmq.PublisherCollector — same inject-at-construction pattern.
type RefreshCollector interface {
	// RecordRefresh increments the refresh counter.
	// success=true means discovery succeeded and a.provider was updated;
	// success=false means discovery failed (fail-open: old provider kept).
	RecordRefresh(success bool)
}

// NoopRefreshCollector is the default collector used when no observability is
// wired. Method body intentionally empty — registration cost is zero and
// metric absence is documented behavior, not a fault.
type NoopRefreshCollector struct{}

// RecordRefresh is a no-op.
func (NoopRefreshCollector) RecordRefresh(_ bool) { /* no-op: metrics disabled */ }

// Compile-time interface check.
var _ RefreshCollector = NoopRefreshCollector{}

// providerRefreshCollector implements RefreshCollector via a provider-neutral
// metrics.Provider. Wired at the composition root with a real Prom provider in
// production and metricsmock in tests.
//
// Metric (subsystem=oidc):
//
//	oidc_jwks_refresh_total (counter, labels: cell, result)
//
// result ∈ {success, failure} — closed set.
// Alerting rule example: rate(oidc_jwks_refresh_total{result="success"}[26h]) == 0
// signals that no successful discovery occurred within one full interval window.
//
// ref: adapters/rabbitmq.providerPublisherCollector — same inject-at-construction
// pattern, same provider-neutral surface.
type providerRefreshCollector struct {
	cellID  string
	refresh metrics.CounterVec
}

var _ RefreshCollector = (*providerRefreshCollector)(nil)

// NewProviderRefreshCollector registers the oidc_jwks_refresh_total counter on
// p and returns a RefreshCollector backed by it. Returns error when cellID is
// empty or when the Provider reports registration failure (typically duplicate
// metric names).
func NewProviderRefreshCollector(p metrics.Provider, cellID string) (RefreshCollector, error) {
	if cellID == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"oidc: cellID is required for provider refresh collector")
	}
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"oidc: metrics.Provider is required")
	}
	refresh, err := p.CounterVec(metrics.CounterOpts{
		Name: "oidc_jwks_refresh_total",
		Help: "Total number of periodic OIDC JWKS re-discovery attempts, by result. " +
			"result=success: provider metadata was refreshed successfully; " +
			"result=failure: re-discovery failed (fail-open: old provider kept). " +
			"Alert on rate(oidc_jwks_refresh_total{result=\"success\"}[26h]) == 0 " +
			"to detect stale discovery metadata.",
		LabelNames: []string{"cell", "result"},
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"oidc: register jwks refresh counter", err)
	}
	return &providerRefreshCollector{cellID: cellID, refresh: refresh}, nil
}

// RecordRefresh increments oidc_jwks_refresh_total{cell, result}.
func (c *providerRefreshCollector) RecordRefresh(success bool) {
	result := "failure"
	if success {
		result = "success"
	}
	c.refresh.With(metrics.Labels{"cell": c.cellID, "result": result}).Inc()
}
