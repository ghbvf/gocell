package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	metadatatest "github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

// TestValidateLifecyclePhase covers CELL-LIFECYCLE-01: cell/slice `lifecycle`
// must be a valid maturity lifecycle (membership), and a slice's lifecycle must
// not exceed its parent cell's lifecycle (empty defaults to experimental,
// matching the BaseCell construction default).
func TestValidateLifecyclePhase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		cellPhase      string
		slicePhase     string
		wantErrorCount int
	}{
		{"both empty - 0 findings", "", "", 0},
		{"valid equal asset - 0 findings", "asset", "asset", 0},
		{"slice downgrade candidate in asset cell - 0 findings", "asset", "candidate", 0},
		{"slice experimental in retired cell - 0 findings", "retired", "experimental", 0},
		{"slice asset in experimental cell - 1 error", "experimental", "asset", 1},
		{"slice candidate in undeclared(experimental) cell - 1 error", "", "candidate", 1},
		{"invalid cell phase - 1 error", "stable", "", 1},
		{"invalid slice phase - 1 error", "asset", "bogus", 1},
		{"contract lifecycle value on cell rejected - 1 error", "active", "", 1},
		{"slice retired in asset cell - 1 error", "asset", "retired", 1},
		{"invalid cell phase + mature slice → only cell finding", "stable", "asset", 1},
		// maintenance cases
		{"slice maintenance in candidate cell - 1 error", "candidate", "maintenance", 1},
		{"cell maintenance valid - 0 findings", "maintenance", "candidate", 0},
		{"both maintenance - 0 findings", "maintenance", "maintenance", 0},
		{"slice maintenance in asset cell - 1 error", "asset", "maintenance", 1},
		{"slice maintenance in maintenance cell - 0 findings", "maintenance", "maintenance", 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			project := &metadata.ProjectMeta{
				Cells: map[string]*metadata.CellMeta{
					metadatatest.CellIDTestCell: {ID: metadatatest.CellIDTestCell, ConsistencyLevel: "L2", Lifecycle: tt.cellPhase},
				},
				Slices: map[string]*metadata.SliceMeta{
					metadatatest.CellIDTestCell + "/testslice": {
						ID: "testslice", BelongsToCell: metadatatest.CellIDTestCell,
						ConsistencyLevel: "L1", Lifecycle: tt.slicePhase,
					},
				},
				Contracts:  map[string]*metadata.ContractMeta{},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}
			v := NewValidator(project, ".", clock.Real())
			results := v.validateCELLLIFECYCLE01()

			var errCount int
			for _, r := range results {
				if r.Severity == SeverityError {
					errCount++
					assert.Equal(t, RuleCode("CELL-LIFECYCLE-01"), r.Code)
					assert.Equal(t, "lifecycle", r.Field)
				}
			}
			assert.Equal(t, tt.wantErrorCount, errCount, "unexpected error count")
		})
	}
}

// TestValidateLifecyclePhase_MissingParentCell verifies a slice with no
// registered parent cell is skipped for the slice≤cell check (REF-01 covers
// the missing cell) but still membership-checked.
func TestValidateLifecyclePhase_MissingParentCell(t *testing.T) {
	t.Parallel()
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{
			"ghost/orphan": {ID: "orphan", BelongsToCell: metadatatest.NewCellID("ghost"), ConsistencyLevel: "L1", Lifecycle: "asset"},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	results := NewValidator(project, ".", clock.Real()).validateCELLLIFECYCLE01()
	// valid lifecycle + missing parent → no findings from this rule
	for _, r := range results {
		assert.NotEqual(t, SeverityError, r.Severity, "orphan slice with valid lifecycle must not error here")
	}
}

// TestValidateLifecyclePhase_OrphanSliceInvalidLifecycle verifies that an
// orphan slice (no registered parent cell) with an invalid lifecycle value is
// flagged with a membership error (the slice≤cell check is skipped for orphans).
func TestValidateLifecyclePhase_OrphanSliceInvalidLifecycle(t *testing.T) {
	t.Parallel()
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{
			"ghost/orphan": {ID: "orphan", BelongsToCell: metadatatest.NewCellID("ghost"), ConsistencyLevel: "L1", Lifecycle: "bogus"},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	results := NewValidator(project, ".", clock.Real()).validateCELLLIFECYCLE01()
	var errCount int
	for _, r := range results {
		if r.Severity == SeverityError {
			errCount++
			assert.Equal(t, RuleCode("CELL-LIFECYCLE-01"), r.Code)
			assert.Equal(t, "lifecycle", r.Field)
		}
	}
	assert.Equal(t, 1, errCount, "orphan slice with invalid lifecycle should produce exactly 1 membership error")
}
