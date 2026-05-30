package accesscore_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/platform/accesscore"
	"github.com/ghbvf/gocell/runtime/composition"
)

func TestModule_ReturnsNonNilCellModule(t *testing.T) {
	m := accesscore.Module()
	require.NotNil(t, m, "Module() must return a non-nil composition.CellModule")
}

func TestModule_CorrectID(t *testing.T) {
	m := accesscore.Module()
	assert.Equal(t, "accesscore", m.ID(), "Module ID must be 'accesscore'")
}

func TestModule_ImplementsCellModule(t *testing.T) {
	var _ composition.CellModule = accesscore.Module()
}
