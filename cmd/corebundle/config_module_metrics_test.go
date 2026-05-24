package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptervault "github.com/ghbvf/gocell/adapters/vault"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// TestConfigCoreModule_Provide_ReplacesKeyProviderMetricsOnRepeatedProvide
// verifies that two Provide calls against the same SharedDeps do not
// double-register vault transit metric collectors (which are now owned by
// SharedDeps.VaultTransitMetrics, not by the KeyProvider), and that cumulative
// state written to VaultTransitMetrics survives across re-Provide.
func TestConfigCoreModule_Provide_ReplacesKeyProviderMetricsOnRepeatedProvide(t *testing.T) {
	shared := newValidatedSharedDeps(t, bootstrap.Topology{StorageBackend: "memory", AdapterMode: "dev"})
	ctx := context.Background()

	// Populate VaultTransitMetrics on shared so the vault-transit path (if taken)
	// and the metrics scrape below work correctly. This mirrors what
	// buildSharedMetricsDeps does at boot.
	vtm, err := adaptervault.NewTransitMetrics(shared.PromStack.registry)
	require.NoError(t, err, "NewTransitMetrics must succeed against a fresh registry")
	shared.VaultTransitMetrics = vtm

	// First Provide — uses a plain fakeKeyProvider (no Metrics method; metrics are
	// now owned by SharedDeps, not by the provider).
	_, _, _, err = ConfigCoreModule{
		KeyProviderOverride: &fakeKeyProvider{},
	}.Provide(ctx, shared)
	require.NoError(t, err)

	// Drive the cached-version atomic through the public funnel.
	shared.VaultTransitMetrics.StoreCachedVersion(1)
	assertCachedKeyVersionFromRegistry(t, shared.PromStack.registry, 1)

	// Second Provide — must not double-register any vault metric and must not
	// disturb the cached-version value set above.
	_, _, _, err = ConfigCoreModule{
		KeyProviderOverride: &fakeKeyProvider{},
	}.Provide(ctx, shared)
	require.NoError(t, err)

	// Cached version is still 1 — the re-Provide must not reset or re-register
	// the gauge that backs it.
	assertCachedKeyVersionFromRegistry(t, shared.PromStack.registry, 1)

	// Advance to 2 to confirm the gauge is still live and writable.
	shared.VaultTransitMetrics.StoreCachedVersion(2)
	assertCachedKeyVersionFromRegistry(t, shared.PromStack.registry, 2)
}

func assertCachedKeyVersionFromRegistry(t *testing.T, registry *prom.Registry, version float64) {
	t.Helper()
	const (
		helpLine = "# HELP gocell_vault_cached_key_version " +
			"Latest Vault Transit key version cached by this process; 0 means cache miss."
		typeLine   = "# TYPE gocell_vault_cached_key_version gauge"
		metricName = `gocell_vault_cached_key_version`
	)
	metricText := helpLine + "\n" + typeLine + "\n" +
		fmt.Sprintf("%s %g\n", metricName, version)
	expected := strings.NewReader(metricText)
	require.NoError(t, testutil.GatherAndCompare(registry, expected, "gocell_vault_cached_key_version"))
}

// TestConfigCoreModule_Provide_FailsFastOnTransitMetricsRegistrationConflict
// verifies that buildSharedMetricsDeps fails fast when a conflicting vault
// transit metric is already registered in the Prometheus registry, rather than
// silently succeeding with a stale collector.
//
// Since TransitMetrics are registered eagerly in buildSharedMetricsDeps (before
// any Provide call), the conflict surface is buildSharedMetricsDeps itself.
func TestConfigCoreModule_Provide_FailsFastOnTransitMetricsRegistrationConflict(t *testing.T) {
	// Pre-register a collector that conflicts with one of the vault transit
	// metrics. gocell_vault_token_renew_success_total is the first collector
	// registered by NewTransitMetrics.
	reg := prom.NewRegistry()
	conflicting := prom.NewCounter(prom.CounterOpts{
		Namespace: "gocell",
		Subsystem: "vault",
		Name:      "token_renew_success_total",
		Help:      "conflict",
	})
	require.NoError(t, reg.Register(conflicting), "pre-registration of conflicting metric must succeed")

	// buildSharedMetricsDeps uses buildPromStack() which constructs a fresh
	// isolated registry internally. We can't inject our pre-fouled registry into
	// buildSharedMetricsDeps directly, so we test NewTransitMetrics directly — the
	// same call that buildSharedMetricsDeps delegates to — against the fouled reg.
	_, err := adaptervault.NewTransitMetrics(reg)
	require.Error(t, err, "NewTransitMetrics must fail when a conflicting collector is already registered")
	assert.Contains(t, err.Error(), "vault", "error must identify the vault metric domain")
}
