package auditcore_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/platform/auditcore"
	"github.com/ghbvf/gocell/runtime/composition"
)

func TestModule_ReturnsNonNilCellModule(t *testing.T) {
	m := auditcore.Module()
	require.NotNil(t, m, "Module() must return a non-nil composition.CellModule")
}

func TestModule_CorrectID(t *testing.T) {
	m := auditcore.Module()
	assert.Equal(t, "auditcore", m.ID(), "Module ID must be 'auditcore'")
}

func TestModule_ImplementsCellModule(t *testing.T) {
	var _ composition.CellModule = auditcore.Module()
}
