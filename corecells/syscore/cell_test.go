package syscore

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
)

func TestNew_Identity(t *testing.T) {
	c := New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.ID() != "syscore" {
		t.Fatalf("ID() = %q, want syscore", c.ID())
	}
	var _ cell.Cell = c // compile-time: SysCore satisfies cell.Cell
}

// TestInitInternal_WiresHandler pins that the hand-written init hook constructs
// the slice handler the generated route group references (before BaseCell.Init,
// reg are irrelevant — initInternal takes no external I/O).
func TestInitInternal_WiresHandler(t *testing.T) {
	c := New()
	if err := c.initInternal(context.Background(), nil); err != nil {
		t.Fatalf("initInternal: %v", err)
	}
	if c.healthHandler == nil {
		t.Fatal("healthHandler is nil after initInternal — generated route group would nil-deref")
	}
	if c.systemHandler == nil {
		t.Fatal("systemHandler is nil after initInternal — generated route group would nil-deref")
	}
}

// TestInit_RegistersAdminRouteGroup runs the full generated Init through a
// RegistryRecorder and pins that the cell registers exactly one route group on
// the PrimaryListener under the /api/v1/admin prefix. This is the regression lock
// for the distinct-prefix requirement: configcore owns the bare /api/v1, so
// syscore MUST own its own segment (a shared prefix fails bootstrap with
// "duplicate route ownership").
func TestInit_RegistersAdminRouteGroup(t *testing.T) {
	c := New()
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	if err := c.Init(context.Background(), rec); err != nil {
		t.Fatalf("Init: %v", err)
	}
	groups := rec.Snapshot().RouteGroups
	if len(groups) != 1 {
		t.Fatalf("RouteGroups = %d, want 1", len(groups))
	}
	if groups[0].Listener != cell.PrimaryListener {
		t.Fatalf("listener = %v, want PrimaryListener", groups[0].Listener)
	}
	if groups[0].Prefix != "/api/v1/admin" {
		t.Fatalf("prefix = %q, want /api/v1/admin (distinct from configcore's /api/v1)", groups[0].Prefix)
	}
}
