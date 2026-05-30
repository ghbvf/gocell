package configcore_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/platform/configcore"
	"github.com/ghbvf/gocell/runtime/composition"
)

func TestModule_ReturnsNonNilCellModule(t *testing.T) {
	m := configcore.Module()
	require.NotNil(t, m, "Module() must return a non-nil composition.CellModule")
}

func TestModule_CorrectID(t *testing.T) {
	m := configcore.Module()
	assert.Equal(t, "configcore", m.ID(), "Module ID must be 'configcore'")
}

func TestModule_ImplementsCellModule(t *testing.T) {
	var _ composition.CellModule = configcore.Module()
}

func TestModule_WithVaultMetrics_DoesNotPanic(t *testing.T) {
	m := configcore.Module(configcore.WithVaultMetrics(nil))
	require.NotNil(t, m)
}

func TestModule_WithKeyProviderOverride_DoesNotPanic(t *testing.T) {
	m := configcore.Module(configcore.WithKeyProviderOverride(nil))
	require.NotNil(t, m)
}
