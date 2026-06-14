//go:build integration

package vault_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	vaultadapter "github.com/ghbvf/gocell/adapters/vault"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
)

// mustTransitMetrics constructs a *TransitMetrics backed by the kernel NopProvider.
// Integration tests only need a valid *TransitMetrics to exercise vault behavior
// against a live Vault server; they do not scrape metric values. Using NopProvider
// (instead of a real Prometheus-backed provider) removes the adapters/prometheus
// and prometheus/client_golang dependency from the vault module graph (#1909).
// Shared across integration_test.go and readiness_test.go.
func mustTransitMetrics(t *testing.T) *vaultadapter.TransitMetrics {
	t.Helper()
	m, err := vaultadapter.NewTransitMetrics(metrics.NopProvider{})
	require.NoError(t, err, "NewTransitMetrics on NopProvider must succeed")
	return m
}
