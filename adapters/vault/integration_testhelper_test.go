//go:build integration

package vault_test

import (
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	vaultadapter "github.com/ghbvf/gocell/adapters/vault"
)

// mustTransitMetrics constructs a *TransitMetrics against a fresh Prometheus
// registry. Integration tests cannot share registries (duplicate collector
// registration), so each constructor needs its own metric set. Shared across
// integration_test.go and readiness_test.go.
func mustTransitMetrics(t *testing.T) *vaultadapter.TransitMetrics {
	t.Helper()
	m, err := vaultadapter.NewTransitMetrics(prom.NewRegistry())
	require.NoError(t, err, "NewTransitMetrics on fresh registry must succeed")
	return m
}
