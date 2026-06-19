package enrollcell

import (
	"context"
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/metadata"

	"github.com/ghbvf/gocell-mdm/cells/enrollcell/slices/status"
)

// nilStatusRepo is a no-op status.Repository for tests that don't exercise the repo.
type nilStatusRepo struct{}

func (nilStatusRepo) ActiveByDeviceID(_ context.Context, _ string) (status.CertRecord, bool, error) {
	return status.CertRecord{}, false, nil
}

// TestEnrollCell_IdentityAndInit covers the cell's identity and that Init
// is a clean no-op (BaseCell.Init only — no routes/slices/probes registered).
func TestEnrollCell_IdentityAndInit(t *testing.T) {
	c := NewEnrollCell(Deps{StatusRepo: nilStatusRepo{}})

	if got := c.ID(); got != "enrollcell" {
		t.Fatalf("ID() = %q, want %q", got, "enrollcell")
	}
	if got := c.Type(); got != cellvocab.CellTypeCore {
		t.Fatalf("Type() = %q, want %q", got, cellvocab.CellTypeCore)
	}

	// BaseCell.Init ignores the Registrar (nil is acceptable for a cell
	// that registers nothing via Init); it only transitions lifecycle state.
	if err := c.Init(context.Background(), nil); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
}

// TestEnrollCell_Startup is the verify.smoke.enrollcell.startup target.
func TestEnrollCell_Startup(t *testing.T) {
	c := NewEnrollCell(Deps{StatusRepo: nilStatusRepo{}})
	ctx := context.Background()

	if err := c.Init(ctx, nil); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !c.Ready() {
		t.Fatal("Ready() = false after Start, want true")
	}
	if err := c.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

// TestEnrollCell_Module covers the NewModule(Deps) path: module ID matches cell ID,
// Provide returns the constructed cell with no opts/resources.
func TestEnrollCell_Module(t *testing.T) {
	m := NewModule(Deps{StatusRepo: nilStatusRepo{}})
	if got := m.ID(); got != cellID {
		t.Fatalf("NewModule().ID() = %q, want %q", got, cellID)
	}

	res, err := m.Provide(context.Background(), nil)
	if err != nil {
		t.Fatalf("Provide() error = %v", err)
	}
	if res.Cell == nil {
		t.Fatal("Provide() returned a nil Cell")
	}
	if got := res.Cell.ID(); got != cellID {
		t.Fatalf("provided cell ID = %q, want %q", got, cellID)
	}
	if len(res.Opts) != 0 {
		t.Errorf("cell should contribute no bootstrap opts, got %d", len(res.Opts))
	}
	if len(res.Resources) != 0 {
		t.Errorf("cell should open no managed resources, got %d", len(res.Resources))
	}
}

// TestEnrollCell_AuthorizerNonNil verifies that Authorizer() returns a non-nil
// auth.Authorizer that bootstrap.PrimaryAuthorizerOption can discover.
func TestEnrollCell_AuthorizerNonNil(t *testing.T) {
	c := NewEnrollCell(Deps{StatusRepo: nilStatusRepo{}})
	a := c.Authorizer()
	if a == nil {
		t.Fatal("Authorizer() returned nil; PrimaryAuthorizerOption cannot discover it")
	}
	// Verify the returned value satisfies auth.Authorizer (return type already
	// enforces this at compile time; the blank-use below documents the intent).
	_ = a
}

// TestEnrollCell_MetadataMatchesYAML is the drift guard for the hand-written
// cellMeta literal vs cell.yaml.
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
