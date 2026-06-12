//go:build integration

package vault_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	vaultadapter "github.com/ghbvf/gocell/adapters/vault"
)

// mustTransitMetrics constructs a *TransitMetrics backed by the kernel NopProvider.
// Integration tests only need a valid *TransitMetrics to exercise vault behaviour
// against a live Vault server; they do not scrape metric values. Using NopProvider
// removes the adapters/prometheus (and prometheus/client_golang) dependency from
// the vault module graph (#1909). The "gocell" namespace mismatch is intentional:
// integration tests do not assert metric values, so the namespace does not matter.
// Shared across integration_test.go and readiness_test.go.
func mustTransitMetrics(t *testing.T) *vaultadapter.TransitMetrics {
	t.Helper()
	m, err := vaultadapter.NewTransitMetrics(metrics.NopProvider{})
	require.NoError(t, err, "NewTransitMetrics on NopProvider must succeed")
	return m
}
