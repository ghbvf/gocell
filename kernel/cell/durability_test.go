package cell

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// stubNoop implements Nooper for testing.
type stubNoop struct{}

func (stubNoop) Noop() bool { return true }

// stubReal does not implement Nooper.
type stubReal struct{}

func TestDurabilityMode_String(t *testing.T) {
	assert.Equal(t, "demo", DurabilityDemo.String())
	assert.Equal(t, "durable", DurabilityDurable.String())
	assert.Equal(t, "unset", DurabilityMode(0).String())
}

func TestCheckNotNoop_UnsetMode_RejectsAll(t *testing.T) {
	// Zero-value DurabilityMode (unset) must be rejected regardless of deps,
	// forcing callers to explicitly choose Demo or Durable.
	// ref: Vault StoredKeysInvalid=0, gRPC InvalidSecurityLevel=0
	err := CheckNotNoop(DurabilityMode(0), "test-cell", stubReal{})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, err.Error(), "invalid DurabilityMode 0")
}

func TestCheckNotNoop_UnsetMode_RejectsEvenWithNoDeps(t *testing.T) {
	err := CheckNotNoop(DurabilityMode(0), "test-cell")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid DurabilityMode 0")
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

func TestCheckNotNoop_InvalidMode_Rejects(t *testing.T) {
	// Non-zero, non-valid mode (e.g., 99) must be rejected, not silently treated as demo.
	// ref: Kubernetes allowlist validation, Uber fx fail-fast
	err := CheckNotNoop(DurabilityMode(99), "test-cell", stubReal{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid DurabilityMode 99")
}

func TestValidateMode(t *testing.T) {
	assert.NoError(t, ValidateMode(DurabilityDemo))
	assert.NoError(t, ValidateMode(DurabilityDurable))
	assert.Error(t, ValidateMode(DurabilityMode(0)))
	assert.Error(t, ValidateMode(DurabilityMode(99)))
	assert.Error(t, ValidateMode(DurabilityMode(-1)))
}
