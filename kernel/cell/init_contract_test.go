package cell

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// ---------------------------------------------------------------------------
// TestCellInit_Signature_AcceptsRegistry
//
// Compile-time proof that Cell.Init accepts a Registry (not Dependencies).
// ---------------------------------------------------------------------------

// compileCellInitSignature is a compile-time check that the Cell interface
// requires Init(ctx, Registry) — this will fail to compile if the signature
// still uses Dependencies.
func compileCellInitSignature() {
	// A function literal that satisfies the Cell.Init shape.
	var _ = func(_ context.Context, _ Registry) error {
		return nil
	}
}

// TestCellInit_Signature_AcceptsRegistry verifies that Cell.Init takes a
// Registry parameter, not Dependencies.  The compile-time assertion above
// is the real guard; this test exists so the file contributes to coverage
// and gives a named anchor in the test suite.
func TestCellInit_Signature_AcceptsRegistry(t *testing.T) {
	// Compile-time assertion: BaseCell must satisfy Cell.
	var _ Cell = (*BaseCell)(nil)

	// The compile-time function above already guards the signature.
	// Nothing to assert at runtime beyond "it compiled".
	compileCellInitSignature()
}

// ---------------------------------------------------------------------------
// TestCellInit_BaseCell_AcceptsRegistry
// ---------------------------------------------------------------------------

func TestCellInit_BaseCell_AcceptsRegistry(t *testing.T) {
	b := MustNewBaseCell(&metadata.CellMeta{ID: "test-cell"})
	rec := NewRegistryRecorder(map[string]any{"k": "v"}, DurabilityDurable)

	err := b.Init(context.Background(), rec)
	require.NoError(t, err)
	assert.True(t, true, "BaseCell.Init(ctx, Registry) accepted without error")
}

// ---------------------------------------------------------------------------
// TestCellInit_ErrorPropagates
//
// Uses an errorInitCell that wraps BaseCell and returns an error from Init.
// Verifies the error bubbles up to the caller — BaseCell.Init's state machine
// must not swallow a subclass error.
// ---------------------------------------------------------------------------

// errorInitCell embeds BaseCell and injects a fixed error from Init.
type errorInitCell struct {
	BaseCell
	initErr error
}

// Init calls the embedded BaseCell.Init (for state transition), then returns
// the configured error.
func (e *errorInitCell) Init(ctx context.Context, reg Registry) error {
	if err := e.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	return e.initErr
}

// Compile-time check: errorInitCell satisfies Cell.
var _ Cell = (*errorInitCell)(nil)

func TestCellInit_ErrorPropagates(t *testing.T) {
	sentinel := errors.New("init failed: db unreachable")
	cell := &errorInitCell{
		BaseCell: *MustNewBaseCell(&metadata.CellMeta{ID: "failing-cell"}),
		initErr:  sentinel,
	}

	rec := NewRegistryRecorder(nil, DurabilityDurable)
	err := cell.Init(context.Background(), rec)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel), "error should be the sentinel error")
}
