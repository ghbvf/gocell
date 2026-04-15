package cell

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// stubNoop implements Noop for testing.
type stubNoop struct{}

func (stubNoop) IsNoop() bool { return true }

// stubReal does not implement Noop.
type stubReal struct{}

func TestDurabilityMode_String(t *testing.T) {
	assert.Equal(t, "demo", DurabilityDemo.String())
	assert.Equal(t, "durable", DurabilityDurable.String())
}

func TestCheckNotNoop_DemoMode_AllowsNoop(t *testing.T) {
	err := CheckNotNoop(DurabilityDemo, "test-cell", stubNoop{})
	require.NoError(t, err)
}

func TestCheckNotNoop_DurableMode_RejectsNoop(t *testing.T) {
	err := CheckNotNoop(DurabilityDurable, "test-cell", stubNoop{})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, err.Error(), "test-cell")
	assert.Contains(t, err.Error(), "durable mode")
}

func TestCheckNotNoop_DurableMode_AllowsReal(t *testing.T) {
	err := CheckNotNoop(DurabilityDurable, "test-cell", stubReal{})
	require.NoError(t, err)
}

func TestCheckNotNoop_DurableMode_AllowsNil(t *testing.T) {
	err := CheckNotNoop(DurabilityDurable, "test-cell", nil)
	require.NoError(t, err)
}

func TestCheckNotNoop_MultipleDeps_RejectsFirstNoop(t *testing.T) {
	err := CheckNotNoop(DurabilityDurable, "test-cell", stubReal{}, stubNoop{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stubNoop")
}

func TestCheckNotNoop_MultipleDeps_AllReal(t *testing.T) {
	err := CheckNotNoop(DurabilityDurable, "test-cell", stubReal{}, stubReal{})
	require.NoError(t, err)
}
