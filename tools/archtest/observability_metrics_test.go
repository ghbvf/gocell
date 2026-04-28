package archtest

import (
	"testing"

	"github.com/ghbvf/gocell/tools/metricschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMetricLabelErrcodeClassifiersRequireAck enforces OBS-01 in the typed
// tools layer, where go/packages can identify the real errcode classifier
// functions and CI can treat missing machine-readable acknowledgements as a
// merge blocker.
func TestMetricLabelErrcodeClassifiersRequireAck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based observability archtest in -short mode")
	}
	root := findModuleRoot(t)

	diagnostics, err := metricschema.CheckOBS01(root)
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
}
