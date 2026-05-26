package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestValidateLifecyclePhase covers LIFECYCLE-PHASE-01: cell/slice `lifecycle`
// must be a valid maturity phase (membership), and a slice's phase must not
// exceed its parent cell's phase (empty defaults to experimental, matching the
// BaseCell construction default).
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
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			project := &metadata.ProjectMeta{
				Cells: map[string]*metadata.CellMeta{
					"testcell": {ID: "testcell", ConsistencyLevel: "L2", Lifecycle: tt.cellPhase},
				},
				Slices: map[string]*metadata.SliceMeta{
					"testcell/testslice": {
						ID: "testslice", BelongsToCell: "testcell",
						ConsistencyLevel: "L1", Lifecycle: tt.slicePhase,
					},
				},
				Contracts:  map[string]*metadata.ContractMeta{},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}
			v := NewValidator(project, ".", clock.Real())
			results := v.validateLifecyclePhase()

			var errCount int
			for _, r := range results {
				if r.Severity == SeverityError {
					errCount++
					assert.Equal(t, RuleCode("LIFECYCLE-PHASE-01"), r.Code)
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
			"ghost/orphan": {ID: "orphan", BelongsToCell: "ghost", ConsistencyLevel: "L1", Lifecycle: "asset"},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	results := NewValidator(project, ".", clock.Real()).validateLifecyclePhase()
	// valid phase + missing parent → no findings from this rule
	for _, r := range results {
		assert.NotEqual(t, SeverityError, r.Severity, "orphan slice with valid phase must not error here")
	}
}
