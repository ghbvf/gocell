package syscore

import (
	"context"
	"testing"
)

func TestModule_ProvidesStatelessSyscoreCell(t *testing.T) {
	m := Module()
	if m.ID() != "syscore" {
		t.Fatalf("ID() = %q, want syscore", m.ID())
	}
	// Provide consumes no SharedDeps (stateless cell), so a nil bag is fine.
	res, err := m.Provide(context.Background(), nil)
	if err != nil {
		t.Fatalf("Provide returned err: %v", err)
	}
	if res.Cell == nil {
		t.Fatal("ModuleResult.Cell is nil")
	}
	if res.Cell.ID() != "syscore" {
		t.Fatalf("cell ID = %q, want syscore", res.Cell.ID())
	}
	if len(res.Resources) != 0 {
		t.Fatalf("Resources = %d, want 0 (stateless cell opens no managed resources)", len(res.Resources))
	}
}
