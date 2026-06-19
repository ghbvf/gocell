package metrics_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// TestNewSessionCacheCollector_RejectsNilProvider asserts fail-fast on nil
// provider.
func TestNewSessionCacheCollector_RejectsNilProvider(t *testing.T) {
	_, err := obmetrics.NewSessionCacheCollector(nil, "accesscore")
	require.Error(t, err)
}

// TestNewSessionCacheCollector_RejectsEmptyCellID asserts fail-fast on empty
// cellID — a per-cell cache always has exactly one owner, no _runtime fallback.
func TestNewSessionCacheCollector_RejectsEmptyCellID(t *testing.T) {
	_, err := obmetrics.NewSessionCacheCollector(kernelmetrics.NopProvider{}, "")
	require.Error(t, err)
}

// TestNewSessionCacheCollector_RegistersFourCounters asserts exactly the four
// session-cache counters are registered, each with the single `cell` label.
func TestNewSessionCacheCollector_RegistersFourCounters(t *testing.T) {
	p := newSagaSpyProvider()
	_, err := obmetrics.NewSessionCacheCollector(p, "accesscore")
	require.NoError(t, err)

	want := []string{
		"session_cache_hits_total",
		"session_cache_misses_total",
		"session_cache_errors_total",
		"session_cache_revoke_del_errors_total",
	}
	for _, name := range want {
		_, ok := p.counterNames[name]
		assert.Truef(t, ok, "counter %q must be registered", name)
		assert.Equalf(t, []string{"cell"}, p.counterLabels[name],
			"counter %q must have exactly the cell label", name)
	}
	assert.Lenf(t, p.counterNames, len(want),
		"expected exactly %d counters, got %v", len(want), p.counterNames)
}

// TestNewSessionCacheCollector_PartialRegistrationFailure_ReturnsError asserts
// that a mid-sequence registration failure rejects the current wiring and
// returns the metric-specific error.
func TestNewSessionCacheCollector_PartialRegistrationFailure_ReturnsError(t *testing.T) {
	p := newSagaSpyProvider()
	p.failOnName = "session_cache_misses_total" // fail on the 2nd of 3
	_, err := obmetrics.NewSessionCacheCollector(p, "accesscore")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session_cache_misses_total")
	assert.Contains(t, p.counterNames, "session_cache_hits_total",
		"counter registered before the failure records the attempted startup wiring")
}

// TestSessionCacheCollector_RecordsEmitWithCellLabel asserts each Record* method
// increments its counter with the baked cell label.
func TestSessionCacheCollector_RecordsEmitWithCellLabel(t *testing.T) {
	p := newSagaSpyProvider()
	c, err := obmetrics.NewSessionCacheCollector(p, "accesscore")
	require.NoError(t, err)

	ctx := context.Background()
	c.RecordHit(ctx)
	c.RecordHit(ctx)
	c.RecordMiss(ctx)
	c.RecordError(ctx)
	c.RecordRevokeDelError(ctx)

	assert.Len(t, p.counterOps["session_cache_hits_total"], 2)
	assert.Len(t, p.counterOps["session_cache_misses_total"], 1)
	assert.Len(t, p.counterOps["session_cache_errors_total"], 1)
	assert.Len(t, p.counterOps["session_cache_revoke_del_errors_total"], 1)
	for _, rec := range p.counterOps["session_cache_hits_total"] {
		assert.Equal(t, "accesscore", rec.labels["cell"])
	}
}

// TestSessionCacheCollector_NilReceiverSafe asserts a nil collector is a no-op
// (disabled cache wires no collector).
func TestSessionCacheCollector_NilReceiverSafe(t *testing.T) {
	var c *obmetrics.SessionCacheCollector
	assert.NotPanics(t, func() {
		c.RecordHit(context.Background())
		c.RecordMiss(context.Background())
		c.RecordError(context.Background())
		c.RecordRevokeDelError(context.Background())
	})
}
