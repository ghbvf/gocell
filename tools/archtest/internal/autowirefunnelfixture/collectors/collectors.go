//go:build archtest_fixture

// Package collectors provides synthetic metric-collector constructors for the
// BOOTSTRAP-AUTOWIRE-COLLECTOR-FUNNEL-01 planted-bypass fixture. They live in a
// sub-package so references to them in the fixture are SelectorExprs (qualified),
// exactly like the real runtime/observability/metrics + kernel/projection
// constructors — letting the shared detector resolve them via ResolvePackageRef.
package collectors

// Provider is a stand-in for kernelmetrics.Provider.
type Provider interface{ provide() }

// Collector is a stand-in for a constructed metric collector.
type Collector struct{}

// NewAlpha mirrors a metric-collector constructor: (Provider) (T, error).
func NewAlpha(Provider) (Collector, error) { return Collector{}, nil }

// NewBeta mirrors a second metric-collector constructor.
func NewBeta(Provider) (Collector, error) { return Collector{}, nil }
