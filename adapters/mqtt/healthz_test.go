package mqtt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/healthz"
)

// TestProbeReady_StringValue asserts the literal value of the typed const.
// "mqtt_ready" is the stable wire key in dashboards/alerts; a change here
// is a breaking operational change.
func TestProbeReady_StringValue(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "mqtt_ready", ProbeReady.String())
	assert.Equal(t, "mqtt_ready", string(ProbeReady))
}

// TestProbeReady_PassesNewProbeNameValidation verifies that the const satisfies
// the healthz probe name regex (snake_case, no consecutive underscores, etc.)
// so operators don't discover breakage from registering the probe at runtime.
func TestProbeReady_PassesNewProbeNameValidation(t *testing.T) {
	t.Parallel()
	parsed, err := healthz.NewProbeName(string(ProbeReady))
	require.NoError(t, err)
	assert.Equal(t, ProbeReady, parsed)
}
