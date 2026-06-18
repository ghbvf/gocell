package enrollcell

import (
	"context"
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
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

// TestEnrollCell_Module covers the composition.CellModule surface: the module ID
// matches the cell ID (composition.Build enforces this) and Provide returns the
// constructed cell with no opts/resources. PR-0 Provide ignores SharedDeps, so nil
// is a valid argument here.
func TestEnrollCell_Module(t *testing.T) {
	m := Module()
	if got := m.ID(); got != cellID {
		t.Fatalf("Module().ID() = %q, want %q", got, cellID)
	}

	res, err := m.Provide(context.Background(), nil)
	if err != nil {
		t.Fatalf("Provide() error = %v", err)
	}
	if res.Cell == nil {
		t.Fatal("Provide() returned a nil Cell")
	}
	if got := res.Cell.ID(); got != cellID {
		t.Fatalf("provided cell ID = %q, want %q (composition.Build requires cell.ID()==module.ID())", got, cellID)
	}
	if len(res.Opts) != 0 {
		t.Errorf("empty cell should contribute no bootstrap opts, got %d", len(res.Opts))
	}
	if len(res.Resources) != 0 {
		t.Errorf("empty cell should open no managed resources, got %d", len(res.Resources))
	}
}

// TestEnrollCell_MetadataMatchesYAML is the drift guard for the hand-written
// cellMeta literal vs cell.yaml. PR-0 has no codegen, so the two are maintained by
// hand; this asserts they stay byte-identical on the fields governance + runtime
// both read, failing CI if a future edit updates one but not the other. (Removed
// once codegen derives cellMeta from cell.yaml, MDM-PR1+.)
func TestEnrollCell_MetadataMatchesYAML(t *testing.T) {
	raw, err := os.ReadFile("cell.yaml")
	if err != nil {
		t.Fatalf("read cell.yaml: %v", err)
	}
	var fromYAML metadata.CellMeta
	if err := yaml.Unmarshal(raw, &fromYAML); err != nil {
		t.Fatalf("unmarshal cell.yaml: %v", err)
	}

	checks := []struct {
		field     string
		yaml, go_ any
	}{
		{"id", fromYAML.ID, cellMeta.ID},
		{"type", fromYAML.Type, cellMeta.Type},
		{"consistencyLevel", fromYAML.ConsistencyLevel, cellMeta.ConsistencyLevel},
		{"durabilityMode", fromYAML.DurabilityMode, cellMeta.DurabilityMode},
		{"lifecycle", fromYAML.Lifecycle, cellMeta.Lifecycle},
		{"owner", fromYAML.Owner, cellMeta.Owner},
		{"schema", fromYAML.Schema, cellMeta.Schema},
		{"verify", fromYAML.Verify, cellMeta.Verify},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.yaml, c.go_) {
			t.Errorf("cell.yaml/%s (%v) drifted from cellMeta literal (%v) — update cell.go cellMeta", c.field, c.yaml, c.go_)
		}
	}
}
