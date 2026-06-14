package webhooksource

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"
)

func TestBuildValueTransformer_RequiresPostgres(t *testing.T) {
	vt, err := buildValueTransformer("memory", "memory", clock.Real(), kernelmetrics.NopProvider{})
	assert.Nil(t, vt)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "postgres")
}

func TestBuildValueTransformer_PostgresLocalAES(t *testing.T) {
	t.Setenv("GOCELL_WEBHOOK_KEY_PROVIDER", "local-aes")
	t.Setenv("GOCELL_WEBHOOK_MASTER_KEY", strings.Repeat("ab", 32))
	vt, err := buildValueTransformer("postgres", "memory", clock.Real(), kernelmetrics.NopProvider{})
	require.NoError(t, err)
	assert.NotNil(t, vt)
}

func TestBuildValueTransformer_PostgresMissingProviderFailsClosed(t *testing.T) {
	t.Setenv("GOCELL_WEBHOOK_KEY_PROVIDER", "")
	vt, err := buildValueTransformer("postgres", "memory", clock.Real(), kernelmetrics.NopProvider{})
	assert.Nil(t, vt)
	require.Error(t, err)
}

// TestLoadSourceStore_RequiresPostgres exercises the public entry point's
// fail-closed storage gate: with memory storage it returns before touching the
// pool. A struct-literal SharedDeps is sufficient because the gate fires first.
func TestLoadSourceStore_RequiresPostgres(t *testing.T) {
	topo, err := bootstrap.NewTopology("", "memory", false)
	require.NoError(t, err)
	shared := &composition.SharedDeps{
		Topology:        topo,
		Clock:           clock.Real(),
		MetricsProvider: kernelmetrics.NopProvider{},
	}
	store, err := LoadSourceStore(context.Background(), shared)
	assert.Nil(t, store)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}
