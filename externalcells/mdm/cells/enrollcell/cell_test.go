package enrollcell

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
)

// TestEnrollCell_IdentityAndInit covers the empty cell's identity and that Init
// is a clean no-op (BaseCell.Init only — no routes/slices/probes registered).
func TestEnrollCell_IdentityAndInit(t *testing.T) {
	c := NewEnrollCell()

	if got := c.ID(); got != "enrollcell" {
		t.Fatalf("ID() = %q, want %q", got, "enrollcell")
	}
	if got := c.Type(); got != cellvocab.CellTypeCore {
		t.Fatalf("Type() = %q, want %q", got, cellvocab.CellTypeCore)
	}

	// BaseCell.Init ignores the Registrar (nil is acceptable for an empty cell
	// that registers nothing); it only transitions lifecycle state.
	if err := c.Init(context.Background(), nil); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
}
