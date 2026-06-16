package metrics_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
)

// realStubProvider embeds NopProvider for the method set but is a DISTINCT dynamic
// type, so metrics.IsReal must treat it as a real provider (the type assertion
// against NopProvider does not match).
type realStubProvider struct{ metrics.NopProvider }

// TestIsReal pins the single-source "real metrics backend?" predicate shared by the
// HTTP bootstrap path and the gRPC interceptor PDP-metrics wrap (#2008 F8).
func TestIsReal(t *testing.T) {
	t.Parallel()
	assert.False(t, metrics.IsReal(nil), "a nil provider is not real")
	assert.False(t, metrics.IsReal(metrics.NopProvider{}), "the NopProvider sentinel is not real")
	assert.True(t, metrics.IsReal(realStubProvider{}), "a non-Nop provider is real")
}
