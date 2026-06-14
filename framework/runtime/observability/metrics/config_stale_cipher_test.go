package metrics_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

func TestProviderConfigStaleCipherCollector_RejectsNilProvider(t *testing.T) {
	collector, err := obmetrics.NewProviderConfigStaleCipherCollector(nil)
	require.Error(t, err)
	assert.Nil(t, collector)
}

func TestProviderConfigStaleCipherCollector_NopProviderNoPanic(t *testing.T) {
	ctx := context.Background()
	collector, err := obmetrics.NewProviderConfigStaleCipherCollector(kernelmetrics.NopProvider{})
	require.NoError(t, err)

	collector.RecordStaleCipher(ctx)
}

func TestProviderConfigStaleCipherCollector_ReturnsRegistrationError(t *testing.T) {
	collector, err := obmetrics.NewProviderConfigStaleCipherCollector(failingCounterProvider{})
	require.Error(t, err)
	assert.Nil(t, collector)
	assert.Contains(t, err.Error(), "register config_stale_cipher_total")
}

func TestProviderConfigStaleCipherCollector_EmitsExpectedMetric(t *testing.T) {
	ctx := context.Background()
	p := newSpyProvider()
	collector, err := obmetrics.NewProviderConfigStaleCipherCollector(p)
	require.NoError(t, err)

	collector.RecordStaleCipher(ctx)

	ops := p.counterOps["config_stale_cipher_total"]
	require.Len(t, ops, 1)
	assert.Empty(t, ops[0].labels, "config_stale_cipher_total is an unlabeled counter")
	assert.Equal(t, 1.0, ops[0].value)

	collector.RecordStaleCipher(ctx)

	ops = p.counterOps["config_stale_cipher_total"]
	require.Len(t, ops, 2)
	assert.Equal(t, 1.0, ops[1].value)
}

func TestNoopConfigStaleCipherCollector_NoPanic(t *testing.T) {
	obmetrics.NoopConfigStaleCipherCollector{}.RecordStaleCipher(context.Background())
}
