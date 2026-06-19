// Package enrollcell is the MDM enrollment cell. PR-0 (#2304) ships it EMPTY — no
// contracts, routes, slices, or events — just enough to compose into the demo
// topology and serve green /healthz + /readyz. The device-identity / certificate /
// enrollment-saga wiring lands in MDM-PR1+ (epic #2299). It is a non-codegen cell
// (hand-written metadata + Init); GoStructName is intentionally left empty.
package enrollcell

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/runtime/composition"
)

// cellID is the single source for both the cell metadata ID and the module ID.
// composition.Build enforces cell.ID() == module.ID() (M12a closed-set identity).
const cellID = "enrollcell"

var _ cell.Cell = (*EnrollCell)(nil)

// EnrollCell is the (currently empty) MDM enrollment cell.
type EnrollCell struct {
	*cell.BaseCell
}

// cellMeta mirrors cells/enrollcell/cell.yaml. Hand-written because PR-0 has no
// codegen; cell.go's constructor passes a Clone so callers cannot mutate the literal.
var cellMeta = &metadata.CellMeta{
	ID:               cellID,
	Type:             "core",
	ConsistencyLevel: "L2",
	DurabilityMode:   "demo",
	Lifecycle:        "experimental",
	Owner:            metadata.OwnerMeta{Team: "mdm", Role: "enrollcell-owner"},
	Schema:           metadata.SchemaMeta{Primary: "mdm_enroll"},
	Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.enrollcell.startup"}},
}

// NewEnrollCell constructs the empty enrollment cell. It has no injectable
// dependencies (PR-0 wires nothing), so the constructor takes no options.
func NewEnrollCell() *EnrollCell {
	return &EnrollCell{BaseCell: cell.MustNewBaseCell(cellMeta.Clone())}
}

// Init is a clean no-op beyond BaseCell.Init: PR-0 registers no routes, slices,
// subscribers, or probes. /healthz and /readyz turn green from BaseCell's own
// Health()/Ready() once bootstrap transitions the cell to the started state.
func (c *EnrollCell) Init(ctx context.Context, reg cell.Registrar) error {
	return c.BaseCell.Init(ctx, reg)
}

// Module returns the composition.CellModule for the enrollment cell.
func Module() composition.CellModule { return module{} }

type module struct{}

func (module) ID() string { return cellID }

// Provide constructs the empty cell. PR-0 opens no resources and needs no extra
// bootstrap options, so ModuleResult carries only the Cell.
func (module) Provide(_ context.Context, _ *composition.SharedDeps) (composition.ModuleResult, error) {
	return composition.ModuleResult{Cell: NewEnrollCell()}, nil
}
