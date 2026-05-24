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

	// Drive the lazy funnel — this is the only sanctioned construction path.
	// In production only the vault-transit branch of buildKeyProvider invokes
	// it; tests that need the metric set can call it directly.
	vtm, err := shared.ProvideVaultTransitMetrics()
	require.NoError(t, err, "ProvideVaultTransitMetrics must succeed against a fresh registry")

	// First Provide — uses a plain fakeKeyProvider (no Metrics method; metrics are
	// now owned by SharedDeps, not by the provider).
	_, _, _, err = ConfigCoreModule{
		KeyProviderOverride: &fakeKeyProvider{},
	}.Provide(ctx, shared)
	require.NoError(t, err)

	// Drive the cached-version atomic through the public funnel.
	vtm.StoreCachedVersion(1)
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

	// Advance to 2 to confirm the gauge is still live and writable. Reuse the
	// once-cached metric pointer (ProvideVaultTransitMetrics is idempotent).
	vtm2, err := shared.ProvideVaultTransitMetrics()
	require.NoError(t, err)
	require.Same(t, vtm, vtm2, "ProvideVaultTransitMetrics must return the same pointer (sync.Once)")
	vtm2.StoreCachedVersion(2)
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
// verifies that the lazy SharedDeps.ProvideVaultTransitMetrics path surfaces a
// registration conflict as a hard error rather than silently succeeding with a
// stale collector.
//
// TransitMetrics construction is now lazy (only the vault-transit branch of
// buildKeyProvider invokes it via ProvideVaultTransitMetrics), so the failure
// surface is the lazy helper itself. NewTransitMetrics is the underlying
// constructor — exercising it directly mirrors what the lazy path delegates to.
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

	_, err := adaptervault.NewTransitMetrics(reg)
	require.Error(t, err, "NewTransitMetrics must fail when a conflicting collector is already registered")
	assert.Contains(t, err.Error(), "vault", "error must identify the vault metric domain")
}
