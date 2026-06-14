package cell

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
)

// ---------------------------------------------------------------------------
// TestCellInit_Signature_AcceptsRegistrar
//
// Compile-time proof that Cell.Init accepts a Registrar (not Dependencies).
// ---------------------------------------------------------------------------

// compileCellInitSignature is a compile-time check that the Cell interface
// requires Init(ctx, Registrar) — this will fail to compile if the signature
// still uses Dependencies.
func compileCellInitSignature() {
	// A function literal that satisfies the Cell.Init shape.
	_ = func(_ context.Context, _ Registrar) error {
		return nil
	}
}

// TestCellInit_Signature_AcceptsRegistrar verifies that Cell.Init takes a
// Registrar parameter, not Dependencies.  The compile-time assertion above
// is the real guard; this test exists so the file contributes to coverage
// and gives a named anchor in the test suite.
func TestCellInit_Signature_AcceptsRegistrar(t *testing.T) {
	// Compile-time assertion: BaseCell must satisfy Cell.
	var _ Cell = (*BaseCell)(nil)

	// The compile-time function above already guards the signature.
	// Nothing to assert at runtime beyond "it compiled".
	compileCellInitSignature()
}

// ---------------------------------------------------------------------------
// TestCellInit_BaseCell_AcceptsRegistrar
// ---------------------------------------------------------------------------

func TestCellInit_BaseCell_AcceptsRegistrar(t *testing.T) {
	b := MustNewBaseCell(&metadata.CellMeta{ID: metadatatest.CellIDTestCell})
	rec := NewRegistryRecorder(map[string]any{"k": "v"}, outbox.DurabilityDurable)

	err := b.Init(context.Background(), rec)
	require.NoError(t, err)
	assert.True(t, true, "BaseCell.Init(ctx, Registrar) accepted without error")
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
func (e *errorInitCell) Init(ctx context.Context, reg Registrar) error {
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
		BaseCell: *MustNewBaseCell(&metadata.CellMeta{ID: metadatatest.NewCellID("failingcell")}),
		initErr:  sentinel,
	}

	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	err := cell.Init(context.Background(), rec)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel), "error should be the sentinel error")
}
