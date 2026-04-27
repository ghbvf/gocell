package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type cachedVersionMetricsProvider struct {
	fakeKeyProvider
	version float64
}

func (p *cachedVersionMetricsProvider) Metrics() []prom.Collector {
	return []prom.Collector{
		prom.NewGaugeFunc(prom.GaugeOpts{
			Namespace: "gocell",
			Subsystem: "vault",
			Name:      "cached_key_version",
			Help:      "Latest Vault Transit key version cached by this process; 0 means cache miss.",
			ConstLabels: prom.Labels{
				"mount_path": "transit",
				"key_name":   "gocell-config",
			},
		}, func() float64 {
			return p.version
		}),
	}
}

var _ kcrypto.KeyProvider = (*cachedVersionMetricsProvider)(nil)

func TestConfigCoreModule_Provide_ReplacesKeyProviderMetricsOnRepeatedProvide(t *testing.T) {
	shared := newValidatedSharedDeps(t, bootstrap.Topology{StorageBackend: "memory", AdapterMode: "dev"})
	ctx := context.Background()

	_, _, _, err := ConfigCoreModule{
		KeyProviderOverride: &cachedVersionMetricsProvider{version: 1},
	}.Provide(ctx, shared)
	require.NoError(t, err)
	assertCachedKeyVersionFromRegistry(t, shared.PromStack.registry, 1)

	_, _, _, err = ConfigCoreModule{
		KeyProviderOverride: &cachedVersionMetricsProvider{version: 2},
	}.Provide(ctx, shared)
	require.NoError(t, err)
	assertCachedKeyVersionFromRegistry(t, shared.PromStack.registry, 2)
}

func assertCachedKeyVersionFromRegistry(t *testing.T, registry *prom.Registry, version float64) {
	t.Helper()
	expected := strings.NewReader(fmt.Sprintf(`# HELP gocell_vault_cached_key_version Latest Vault Transit key version cached by this process; 0 means cache miss.
# TYPE gocell_vault_cached_key_version gauge
gocell_vault_cached_key_version{key_name="gocell-config",mount_path="transit"} %g
`, version))
	require.NoError(t, testutil.GatherAndCompare(registry, expected, "gocell_vault_cached_key_version"))
}
