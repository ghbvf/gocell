//go:build integration

package vault_test

import (
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	promadapter "github.com/ghbvf/gocell/adapters/prometheus"
	vaultadapter "github.com/ghbvf/gocell/adapters/vault"
)

// mustTransitMetrics constructs a *TransitMetrics against a fresh Prometheus-backed
// metrics.Provider. Integration tests cannot share registries (duplicate collector
// registration), so each constructor needs its own registry/provider. The "gocell"
// namespace matches production so the gocell_vault_* metric names are unchanged.
// Shared across integration_test.go and readiness_test.go.
func mustTransitMetrics(t *testing.T) *vaultadapter.TransitMetrics {
	t.Helper()
	provider, err := promadapter.NewMetricProvider(promadapter.MetricProviderConfig{
		Registry:  prom.NewRegistry(),
		Namespace: "gocell",
	})
	require.NoError(t, err, "NewMetricProvider on fresh registry must succeed")
	m, err := vaultadapter.NewTransitMetrics(provider)
	require.NoError(t, err, "NewTransitMetrics on fresh provider must succeed")
	return m
}
